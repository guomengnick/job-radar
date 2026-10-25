package assess

import "github.com/anthropics/anthropic-sdk-go"

// The rubric. The user's own facts and preferences live in the profile,
// which is private (an SSM parameter), so this file holds no personal data.
const instructions = `你是使用者的求職助理。依 <profile> 裡的背景與偏好，評估使用者送來的一份職缺。你的回覆必須是呼叫一次 record_assessment 工具，不要輸出其他文字。所有欄位用繁體中文。

評分（score，0–100）：
- 80 以上：<profile> 的跳板條件幾乎都符合，技能高度契合，沒有嚴重紅旗。
- 60–79：大致符合，有一兩項要確認（例如薪資沒寫、公司類型不明）。
- 40–59：只部分符合，或有一項明顯紅旗。
- 40 以下：方向不符、有嚴重紅旗，或這頁根本不是職缺（那就給 0，並在 reason 說明）。
verdict：75 以上「建議投」，55–74「可考慮」，其他「不建議」。

各欄位：
- direction_fit（0–10）：工作內容符合 <profile>「想要的工作」所寫方向的程度；標為最重要的那條份量最重。
- skills_fit（0–10）：職缺要求的技能，使用者有多少；使用者沒有的必備技能要扣分，並列入 red_flags。
- company_type：自有產品、大公司軟體部門、外包派遣（含駐點、派遣、人力仲介代徵）、系統整合（替客戶做專案），看不出來就填「不明」。
- salary：照頁面寫的抄，沒寫就填「未寫」。不要推測。
- red_flags：依 <profile> 的紅旗清單，加上你從頁面看到的其他疑慮。每項一短句，最多 5 項。
- evidence：2–4 段頁面原文的短引用（每段 40 字以內），支持你最重要的判斷。只能引用頁面上真的有的字。
- resume：依 <profile> 的版本規則選 A、B 或 C。
- add_ml_line：職缺跟 AI、機器學習、模型、資料科學有關時為 true。
- letter_line：一句推薦信對應句，格式像「這個職缺需要＿＿，跟我＿＿的經驗直接相關。」。只能用 <profile> 裡寫的事實，不可誇大或編造，60 字以內。
- reason：一句話總結為什麼是這個分數，40 字以內。
- job_title、company：從頁面抄。`

const toolName = "record_assessment"

// The tool's input is the Assessment. The schema keeps the reply's shape
// fixed; clean still checks every value, since a schema is advice to the
// model, not a guarantee.
var tool = anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
	Name:        toolName,
	Description: anthropic.String("回報這份職缺的評估結果。"),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"job_title":     map[string]any{"type": "string"},
			"company":       map[string]any{"type": "string"},
			"score":         map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
			"verdict":       map[string]any{"type": "string", "enum": verdicts},
			"direction_fit": map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
			"skills_fit":    map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
			"company_type":  map[string]any{"type": "string", "enum": companyTypes},
			"salary":        map[string]any{"type": "string"},
			"red_flags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 5},
			"evidence":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 4},
			"resume":        map[string]any{"type": "string", "enum": resumes},
			"add_ml_line":   map[string]any{"type": "boolean"},
			"letter_line":   map[string]any{"type": "string"},
			"reason":        map[string]any{"type": "string"},
		},
		Required: []string{"job_title", "company", "score", "verdict", "direction_fit", "skills_fit",
			"company_type", "salary", "red_flags", "evidence", "resume", "add_ml_line", "letter_line", "reason"},
	},
}}
