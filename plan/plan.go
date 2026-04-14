package plan

// Plan represents a subscription tier
type Plan string

const (
	Free   Plan = "free"
	Pro    Plan = "pro"
	Max    Plan = "max"
	Custom Plan = "custom"
)

// BillingPeriod represents the billing cycle
type BillingPeriod string

const (
	Monthly BillingPeriod = "monthly"
	Yearly  BillingPeriod = "yearly"
)

// Star pricing constants (1 Star ≈ $0.013)
const (
	ProMonthlyStars = 1500  // ~$19/mo
	ProYearlyStars  = 15000 // ~$190/yr ($15.83/mo)
	MaxMonthlyStars = 3750  // ~$49/mo
	MaxYearlyStars  = 37500 // ~$490/yr ($40.83/mo)
)

// StarPrice returns the star amount for a plan and billing period. Returns 0 for Free/Custom.
func StarPrice(p Plan, period BillingPeriod) int {
	switch p {
	case Pro:
		if period == Yearly {
			return ProYearlyStars
		}
		return ProMonthlyStars
	case Max:
		if period == Yearly {
			return MaxYearlyStars
		}
		return MaxMonthlyStars
	default:
		return 0
	}
}

// Limits holds the enforced limits for a plan
type Limits struct {
	MonthlyMessages int   // Max AI responses per calendar month
	PerMinute       int   // Max AI responses per rolling 60s window
	KnowledgeUpload bool  // Whether knowledge upload is allowed
	MaxFileSize     int64 // Max file size for knowledge uploads in bytes (0 = not allowed)
	WebSearch       bool  // Whether web_search tool is available
	WebFetch        bool  // Whether web_fetch tool is available
	CreatePost      bool  // Whether manual post creation (/create_post) is available
	SchedulePost    bool  // Whether scheduled automatic posting is available
}

// planLimits maps each plan to its limits
var planLimits = map[Plan]Limits{
	Free: {
		MonthlyMessages: 1000,
		PerMinute:       10,
		KnowledgeUpload: false,
		MaxFileSize:     0,
		WebSearch:       false,
		WebFetch:        false,
		CreatePost:      false,
		SchedulePost:    false,
	},
	Pro: {
		MonthlyMessages: 2500,
		PerMinute:       30,
		KnowledgeUpload: false,
		MaxFileSize:     0,
		WebSearch:       true,
		WebFetch:        true,
		CreatePost:      true,
		SchedulePost:    false,
	},
	Max: {
		MonthlyMessages: 10_000,
		PerMinute:       60,
		KnowledgeUpload: true,
		MaxFileSize:     10 * 1024 * 1024, // 10 MB
		WebSearch:       true,
		WebFetch:        true,
		CreatePost:      true,
		SchedulePost:    true,
	},
	Custom: {
		MonthlyMessages: 100_000,
		PerMinute:       120,
		KnowledgeUpload: true,
		MaxFileSize:     50 * 1024 * 1024, // 50 MB
		WebSearch:       true,
		WebFetch:        true,
		CreatePost:      true,
		SchedulePost:    true,
	},
}

// GetLimits returns the limits for the given plan. Defaults to Free if unknown.
func GetLimits(p Plan) Limits {
	if l, ok := planLimits[p]; ok {
		return l
	}
	return planLimits[Free]
}

// Valid returns true if the plan is a recognized tier
func (p Plan) Valid() bool {
	_, ok := planLimits[p]
	return ok
}

// IsPaid returns true if the plan requires payment
func (p Plan) IsPaid() bool {
	return p == Pro || p == Max
}

// DisplayName returns a human-readable plan name with pricing
func (p Plan) DisplayName() string {
	switch p {
	case Pro:
		return "Pro ($19/mo)"
	case Max:
		return "Max ($49/mo)"
	case Custom:
		return "Custom"
	default:
		return "Free"
	}
}

// ShortName returns just the plan name without price
func (p Plan) ShortName() string {
	switch p {
	case Pro:
		return "Pro"
	case Max:
		return "Max"
	case Custom:
		return "Custom"
	default:
		return "Free"
	}
}
