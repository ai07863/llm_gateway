package pricing

type rate struct {
	in, out float64 // 元 / 百万 token
}

var table = map[string]rate{
	"deepseek-chat": {0.27, 1.10},
	"gpt-4o-mini":   {1.10, 4.40},
	// Alibaba Cloud DashScope qwen 系列（示例价，随官方调价自行更新）
	"qwen-turbo":    {0.30, 0.60},
	"qwen-plus":     {0.80, 2.00},
	"qwen-max":      {2.40, 9.60},
	"qwen3.8-flash": {0.80, 2.70},
}

func Cost(model string, promptTokens, completionTokens int) float64 {
	r, ok := table[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)/1e6*r.in + float64(completionTokens)/1e6*r.out
}

func Score(model string) (float64, bool) {
	r, ok := table[model]
	if !ok {
		return 0, false
	}
	return r.in + r.out, true
}
