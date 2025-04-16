package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
	"os/exec"

	"github.com/joho/godotenv"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_"github.com/glebarez/sqlite"
)

var (
	API_TOKEN       string
	ALLOWED_USER_ID int64
	DB_PATH         string
	categories      = []string
	bot *tgbotapi.BotAPI
	db  *sql.DB
)

type TransactionState struct {
	UserID          int64
	Step            string // Tracks current state step
	TransactionType string // "income" or "expense"
	Category        string
	Amount          float64
	Description     string
}

var userStates = make(map[int64]*TransactionState)

func main() {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	API_TOKEN = os.Getenv("API_TOKEN")
	ALLOWED_USER_ID, _ = strconv.ParseInt(os.Getenv("ALLOWED_USER_ID"), 10, 64)
	DB_PATH = os.Getenv("DB_PATH")

	// Parse categories
	catStr := os.Getenv("CATEGORIES")
	if catStr != "" {
		categories = strings.Split(catStr, ",")
		for i := range categories {
			categories[i] = strings.TrimSpace(categories[i])
		}
	} else {
		categories = []string{
			"Food", "Salary", "Needs", "Water", "Laundry",
			"Transportation", "Utilities", "Rent", "Bills",
		}
	}

	// Initialize bot
	bot, err = tgbotapi.NewBotAPI(API_TOKEN)
	if err != nil {
		log.Panic(err)
	}

	// Initialize database
	db, err = sql.Open("sqlite", DB_PATH)
	if err != nil {
		log.Panic(err)
	}
	defer db.Close()

	// Create transactions table if not exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS transactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		category TEXT NOT NULL,
		amount REAL NOT NULL,
		description TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			handleMessage(update.Message)
		} else if update.CallbackQuery != nil {
			handleCallbackQuery(update.CallbackQuery)
		}
	}
}

func handleMessage(message *tgbotapi.Message) {
	userID := message.From.ID
	if userID != ALLOWED_USER_ID {
		sendMessage(message.Chat.ID, "You are not authorized to use this bot.")
		return
	}

	switch message.Command() {
	case "add":
		startTransaction(message.Chat.ID, userID)
	case "summary":
		showSummary(message.Chat.ID)
	case "get_latest_report":
		get_latest_report(message.Chat.ID)
	case "get_weekly_expense":
		get_weekly_expense_report(message.Chat.ID)
	default:
		if state, exists := userStates[userID]; exists {
			switch state.Step {
			case "ENTER_AMOUNT":
				processAmount(message, state)
			case "ENTER_DESCRIPTION":
				processDescription(message, state)
			}
		} else {
			sendMessage(message.Chat.ID, "I don't understand that command.")
		}
	}
}

func handleCallbackQuery(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	if userID != ALLOWED_USER_ID {
		sendMessage(callback.Message.Chat.ID, "You are not authorized to use this bot.")
		return
	}

	state, exists := userStates[userID]
	if !exists {
		return
	}

	switch state.Step {
	case "SELECT_TYPE":
		processTransactionType(callback, state)
	case "SELECT_CATEGORY":
		processCategory(callback, state)
	}
}

func startTransaction(chatID int64, userID int64) {
	state := &TransactionState{
		UserID: userID,
		Step:   "SELECT_TYPE",
	}
	userStates[userID] = state

	buttons := [][]tgbotapi.InlineKeyboardButton{
		{
			tgbotapi.NewInlineKeyboardButtonData("Income", "income"),
			tgbotapi.NewInlineKeyboardButtonData("Expense", "expense"),
		},
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(buttons...)
	sendMessageWithKeyboard(chatID, "Please choose the type of transaction:", keyboard)
}

func processTransactionType(callback *tgbotapi.CallbackQuery, state *TransactionState) {
	state.TransactionType = callback.Data
	state.Step = "SELECT_CATEGORY"

	buttons := make([][]tgbotapi.InlineKeyboardButton, 0)
	for _, category := range categories {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(category, category),
		))
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(buttons...)
	editMessageWithKeyboard(
		callback.Message.Chat.ID,
		callback.Message.MessageID,
		fmt.Sprintf("You selected %s. Choose a category:", state.TransactionType),
		keyboard,
	)
}

func processCategory(callback *tgbotapi.CallbackQuery, state *TransactionState) {
	state.Category = callback.Data
	state.Step = "ENTER_AMOUNT"

	editMessage(
		callback.Message.Chat.ID,
		callback.Message.MessageID,
		fmt.Sprintf("Selected category: %s. Enter the transaction amount.", state.Category),
	)
}

func processAmount(message *tgbotapi.Message, state *TransactionState) {
	amount, err := strconv.ParseFloat(message.Text, 64)
	if err != nil || amount <= 0 {
		sendMessage(message.Chat.ID, "Invalid amount. Please enter a positive number.")
		return
	}

	state.Amount = amount
	state.Step = "ENTER_DESCRIPTION"
	sendMessage(message.Chat.ID, "Enter a description for the transaction (max 100 characters).")
}

func processDescription(message *tgbotapi.Message, state *TransactionState) {
	if len(message.Text) > 100 {
		sendMessage(message.Chat.ID, "Description too long. Please keep it under 100 characters.")
		return
	}

	state.Description = message.Text

	// Get current time in GMT+7
	currentTime := time.Now().In(time.FixedZone("GMT+7", 7*60*60))

	stmt, err := db.Prepare("INSERT INTO transactions (type, category, amount, description, created_at) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		sendMessage(message.Chat.ID, "Failed to prepare transaction.")
		log.Printf("Database prepare error: %v", err)
		return
	}
	defer stmt.Close()

	_, err = stmt.Exec(state.TransactionType, state.Category, state.Amount, state.Description, currentTime.Format("2006-01-02 15:04:05"))
	if err != nil {
		sendMessage(message.Chat.ID, "Failed to save transaction.")
		log.Printf("Database exec error: %v", err)
		return
	}

	delete(userStates, state.UserID)
	sendMessage(message.Chat.ID, "Transaction added successfully!")
}


func showSummary(chatID int64) {
	currentMonth := time.Now().UTC().Format("01")
	rows, err := db.Query("SELECT type, SUM(amount) as total FROM transactions WHERE strftime('%m', created_at) = ? GROUP BY type", currentMonth)
	if err != nil {
		sendMessage(chatID, "Error retrieving transactions.")
		log.Printf("Database query error: %v", err)
		return
	}
	defer rows.Close()

	incomeTotal := 0.0
	expenseTotal := 0.0
	for rows.Next() {
		var transactionType string
		var total float64
		err := rows.Scan(&transactionType, &total)
		if err != nil {
			log.Printf("Row scan error: %v", err)
			continue
		}
		if transactionType == "income" {
			incomeTotal = total
		} else if transactionType == "expense" {
			expenseTotal = total
		}
	}

	if err = rows.Err(); err != nil {
		log.Printf("Rows error: %v", err)
	}

	balance := incomeTotal - expenseTotal
	summaryMessage := fmt.Sprintf("Monthly Summary Report for %s:\n\n", time.Now().Format("January 2006"))
	summaryMessage += fmt.Sprintf("Total Income: %.2f\nTotal Expense: %.2f\n\nBalance: %.2f", 
		incomeTotal, expenseTotal, balance)
	sendMessage(chatID, summaryMessage)
}

func sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("Error sending message: %v", err)
	}
}

func sendMessageWithKeyboard(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("Error sending message with keyboard: %v", err)
	}
}

func editMessage(chatID int64, messageID int, text string) {
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("Error editing message: %v", err)
	}
}

func editMessageWithKeyboard(chatID int64, messageID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, text, keyboard)
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("Error editing message with keyboard: %v", err)
	}
}

func get_latest_report(chatID int64) {
	cmd := exec.Command("python3", "src/g_latest_r.py") // Path to your Python script
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error executing Python script: %s", err)
		sendMessage(chatID, "Failed to execute the report.")
		return
	}

	sendMessage(chatID, string(output))
}


func get_weekly_expense_report(chatID int64) {
	cmd := exec.Command("python3", "src/g_weekly_e_r.py") // Replace with your Python script path
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error executing Python script: %s", err)
		sendMessage(chatID, "Failed to execute the report.")
		return
	}

	sendMessage(chatID, string(output))
}

