package plan

// Plan represents a subscription tier
type Plan string

const (
	Free Plan = "free"
	Pro  Plan = "pro"
	Max  Plan = "max"
)

// Limits holds the enforced limits for a plan
type Limits struct {
	MonthlyMessages int   // Max AI responses per calendar month
	PerMinute       int   // Max AI responses per rolling 60s window
	KnowledgeUpload bool  // Whether knowledge upload is allowed
	MaxFileSize     int64 // Max file size for knowledge uploads in bytes (0 = not allowed)
}

// planLimits maps each plan to its limits
var planLimits = map[Plan]Limits{
	Free: {
		MonthlyMessages: 1000,
		PerMinute:       10,
		KnowledgeUpload: false,
		MaxFileSize:     0,
	},
	Pro: {
		MonthlyMessages: 10_000,
		PerMinute:       100,
		KnowledgeUpload: true,
		MaxFileSize:     5 * 1024 * 1024, // 5 MB
	},
	Max: {
		MonthlyMessages: 50_000,
		PerMinute:       100,
		KnowledgeUpload: true,
		MaxFileSize:     20 * 1024 * 1024, // 20 MB
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

// DisplayName returns a human-readable plan name
func (p Plan) DisplayName() string {
	switch p {
	case Pro:
		return "Pro ($49/mo)"
	case Max:
		return "Max ($99/mo)"
	default:
		return "Free"
	}
}
