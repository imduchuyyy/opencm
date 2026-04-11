package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/plan"
)

// invoicePayload is encoded in the invoice payload to identify what was purchased
type invoicePayload struct {
	Plan          string `json:"plan"`
	BillingPeriod string `json:"billing_period"`
	ChatID        int64  `json:"chat_id"`
}

// handlePreCheckoutQuery responds to a pre-checkout query within 10 seconds.
// For Telegram Stars, we always approve (validation is minimal).
func (a *Agent) handlePreCheckoutQuery(query *tgbotapi.PreCheckoutQuery) {
	log.Printf("[Payments] PreCheckoutQuery from user %d: currency=%s amount=%d payload=%s",
		query.From.ID, query.Currency, query.TotalAmount, query.InvoicePayload)

	// Parse and validate the payload
	var payload invoicePayload
	if err := json.Unmarshal([]byte(query.InvoicePayload), &payload); err != nil {
		log.Printf("[Payments] Invalid payload: %v", err)
		a.answerPreCheckout(query.ID, false, "Invalid payment data. Please try again.")
		return
	}

	p := plan.Plan(payload.Plan)
	if !p.IsPaid() {
		a.answerPreCheckout(query.ID, false, "Invalid plan selected.")
		return
	}

	period := plan.BillingPeriod(payload.BillingPeriod)
	expectedStars := plan.StarPrice(p, period)
	if expectedStars == 0 || query.TotalAmount != expectedStars {
		log.Printf("[Payments] Amount mismatch: expected %d, got %d", expectedStars, query.TotalAmount)
		a.answerPreCheckout(query.ID, false, "Price mismatch. Please try again.")
		return
	}

	// Verify the user is admin of the target group
	if !a.isAdmin(payload.ChatID, query.From.ID) {
		a.answerPreCheckout(query.ID, false, "You must be an admin of the group to purchase a subscription.")
		return
	}

	// All good - approve
	a.answerPreCheckout(query.ID, true, "")
}

// answerPreCheckout sends the answer to a pre-checkout query
func (a *Agent) answerPreCheckout(queryID string, ok bool, errMsg string) {
	config := tgbotapi.PreCheckoutConfig{
		PreCheckoutQueryID: queryID,
		OK:                 ok,
		ErrorMessage:       errMsg,
	}
	if _, err := a.bot.Request(config); err != nil {
		log.Printf("[Payments] Failed to answer pre-checkout query: %v", err)
	}
}

// handleSuccessfulPayment processes a confirmed payment from Telegram Stars
func (a *Agent) handleSuccessfulPayment(msg *tgbotapi.Message) {
	payment := msg.SuccessfulPayment
	if payment == nil {
		return
	}

	log.Printf("[Payments] SuccessfulPayment from user %d: currency=%s amount=%d charge_id=%s",
		msg.From.ID, payment.Currency, payment.TotalAmount, payment.TelegramPaymentChargeID)

	var payload invoicePayload
	if err := json.Unmarshal([]byte(payment.InvoicePayload), &payload); err != nil {
		log.Printf("[Payments] Invalid payload in successful payment: %v", err)
		a.send(msg.Chat.ID, MsgPaymentErrorGeneric)
		return
	}

	p := plan.Plan(payload.Plan)
	period := plan.BillingPeriod(payload.BillingPeriod)

	// Calculate subscription dates
	now := time.Now().UTC()
	var expiresAt time.Time
	switch period {
	case plan.Yearly:
		expiresAt = now.AddDate(1, 0, 0)
	default: // monthly
		expiresAt = now.AddDate(0, 1, 0)
	}

	// Expire any existing active subscriptions before creating the new one
	if err := a.db.ExpireActiveSubscriptions(payload.ChatID); err != nil {
		log.Printf("[Payments] Error expiring old subscriptions for %d: %v", payload.ChatID, err)
	}

	// Create subscription record
	sub := &database.Subscription{
		ChatID:                  payload.ChatID,
		Plan:                    string(p),
		BillingPeriod:           string(period),
		StarAmount:              payment.TotalAmount,
		TelegramPaymentChargeID: payment.TelegramPaymentChargeID,
		StartedAt:               now,
		ExpiresAt:               expiresAt,
	}

	if err := a.db.CreateSubscription(sub); err != nil {
		log.Printf("[Payments] Failed to create subscription: %v", err)
		a.send(msg.Chat.ID, MsgPaymentErrorGeneric)
		return
	}

	// Update group config plan to match the new subscription
	groupCfg := a.getOrCreateGroupConfig(payload.ChatID)
	groupCfg.Plan = p
	if err := a.db.UpsertGroupConfig(groupCfg); err != nil {
		log.Printf("[Payments] Failed to update group plan: %v", err)
	}

	// Get group title for the confirmation message
	groupTitle := fmt.Sprintf("Group %d", payload.ChatID)
	if g, err := a.db.GetGroup(payload.ChatID); err == nil {
		groupTitle = g.ChatTitle
	}

	periodLabel := "month"
	if period == plan.Yearly {
		periodLabel = "year"
	}

	a.send(msg.Chat.ID, fmt.Sprintf(MsgPaymentSuccess,
		p.ShortName(), groupTitle, payment.TotalAmount, periodLabel,
		expiresAt.Format("Jan 2, 2006")))
}

// sendSubscriptionInvoice sends a Telegram Stars invoice to the user for a plan upgrade
func (a *Agent) sendSubscriptionInvoice(chatID int64, userID int64, targetGroupID int64, p plan.Plan, period plan.BillingPeriod) {
	starAmount := plan.StarPrice(p, period)
	if starAmount == 0 {
		a.send(chatID, MsgPaymentInvalidPlan)
		return
	}

	// Build payload
	payload := invoicePayload{
		Plan:          string(p),
		BillingPeriod: string(period),
		ChatID:        targetGroupID,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[Payments] Failed to marshal payload: %v", err)
		a.send(chatID, MsgPaymentErrorGeneric)
		return
	}

	periodLabel := "Monthly"
	if period == plan.Yearly {
		periodLabel = "Yearly"
	}

	// Get group title
	groupTitle := fmt.Sprintf("Group %d", targetGroupID)
	if g, err := a.db.GetGroup(targetGroupID); err == nil {
		groupTitle = g.ChatTitle
	}

	title := fmt.Sprintf("OpenCM %s Plan (%s)", p.ShortName(), periodLabel)
	description := fmt.Sprintf("%s subscription for %s", p.ShortName(), groupTitle)

	invoice := tgbotapi.NewInvoice(
		chatID,
		title,
		description,
		string(payloadJSON),
		"",    // providerToken - empty for Telegram Stars
		"",    // startParameter - not needed for DM invoices
		"XTR", // currency - Telegram Stars
		[]tgbotapi.LabeledPrice{
			{Label: title, Amount: starAmount},
		},
	)

	if _, err := a.bot.Send(invoice); err != nil {
		log.Printf("[Payments] Failed to send invoice: %v", err)
		a.send(chatID, MsgPaymentErrorSendInvoice)
	}
}

// handleSubscribeCommand processes /subscribe_pro and /subscribe_max commands
func (a *Agent) handleSubscribeCommand(chatID, userID int64, targetPlan plan.Plan, text string) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	if !a.isAdmin(groupChatID, userID) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgNoLongerAdmin)
		return
	}

	// Check if already on this plan or higher
	currentPlan := a.db.GetEffectivePlan(groupChatID)
	if currentPlan == targetPlan {
		a.send(chatID, fmt.Sprintf(MsgAlreadyOnPlan, targetPlan.ShortName()))
		return
	}

	// Default to monthly, check if "yearly" or "year" is in the text
	period := plan.Monthly
	lower := strings.ToLower(text)
	if containsWord(lower, "yearly") || containsWord(lower, "year") || containsWord(lower, "annual") {
		period = plan.Yearly
	}

	// Send the invoice
	a.sendSubscriptionInvoice(chatID, userID, groupChatID, targetPlan, period)
}

// containsWord checks if a string contains a word (case insensitive, simple check)
func containsWord(s, word string) bool {
	for _, w := range splitWords(s) {
		if w == word {
			return true
		}
	}
	return false
}

func splitWords(s string) []string {
	var words []string
	word := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if word != "" {
				words = append(words, word)
				word = ""
			}
		} else {
			word += string(r)
		}
	}
	if word != "" {
		words = append(words, word)
	}
	return words
}
