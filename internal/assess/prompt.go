package assess

import "github.com/anthropics/anthropic-sdk-go"

// The rubric. The user's own facts and preferences live in the profile,
// which is private (an SSM parameter), so this file holds no personal data.
const instructions = `你是使用者的求職助理。依 <profile> 裡的背景與偏好，評估使用者送來的一份職缺。你的回覆必須是呼叫一次 record_assessment 工具，不要輸出其他文字。所有欄位用繁體中文。

評分（score，0–100）：
- 80 以上：<profile> 的跳板條件幾乎都符合，技能高度契合，沒有紅旗，工作內容寫得具體。
- 60–79：大致符合，有一兩項要確認（例如薪資沒寫、公司類型不明），或缺一兩項「其他必備」技能。
- 40–59：只部分符合，或有一項會讓人不投的紅旗（缺核心技能、硬門檻不符、外包派遣、<profile> 紅旗清單上的）。
- 40 以下：方向不符、有嚴重紅旗，或這頁根本不是職缺（那就給 0，並在 reason 說明）。
- 工作內容寫得短不扣分：照寫出來的要求評分，寫出來的都對得上就是契合。沒寫的條件放 to_confirm，不能當成符合，也不能當成不符合。jd_detail 3 以下、skills_fit 6 以上、又沒有紅旗時，score 至少 55。
- 薪資只看頁面寫的：不可從工作內容長短、年資要求或公司大小推測薪資高低，也不能因此扣分。
verdict：75 以上「建議投」，55–74「可考慮」，其他「不建議」。

各欄位：
- direction_fit（0–10）：工作內容符合 <profile>「想要的工作」所寫方向的程度；標為最重要的那條份量最重。
- required_skills：職缺明確要求的技能，逐項列出（最多 12 項），每項標兩件事。weight 是多重要：「核心」＝這份工作每天主要在做的，通常是職稱或工作內容第一項的語言或領域；「其他必備」；「一般」＝Git、溝通、讀英文文件這類；「硬門檻」＝年資、學歷、國籍、證照、語言程度。has 是使用者有沒有：「有」、「相近」（<profile>「可轉換技能」寫了同類經驗）、「沒有」。一般技能除非 <profile> 寫沒有，都算「有」。
- skills_fit（0–10）：依 required_skills 算：有算 1、相近算 0.5、沒有算 0，核心的份量加倍，按比例換成 0–10。核心「沒有」時最多 3；核心有、而且加權後超過一半時至少 6。
- jd_detail（0–10）：工作內容寫得多具體：有沒有寫做什麼產品或系統、具體職責、團隊、用的技術、要解決的問題。只有幾行、只列語言或工具 → 3 以下。
- company_type：自有產品、大公司軟體部門、外包派遣（受雇於人力或外包公司、被派去客戶那裡上班，含駐點、派遣）、系統整合（替客戶做專案），看不出來就填「不明」。人力公司「代企業徵才」不是外包派遣：受雇的是代徵的那家企業，依它判斷。
- salary：照頁面寫的抄，沒寫就填「未寫」。不要推測。
- red_flags：只列看到就可能不投的事：<profile> 的紅旗清單、核心技能「沒有」、硬門檻不符、外包派遣。每項一短句，0–3 項；沒有就空著，不要湊數。缺「其他必備」技能不是紅旗。
- to_confirm：要問清楚、但不至於不投的事，例如薪資面議、公司類型看不出來、工作內容寫得太空泛、缺的其他必備技能實際用多少。每項一短句，0–3 項。
- evidence：2–4 段頁面原文的短引用（每段 40 字以內），支持你最重要的判斷。只能引用頁面上真的有的字。
- resume：依 <profile> 的版本規則選 A、B 或 C。
- add_ml_line：職缺跟 AI、機器學習、模型、資料科學有關時為 true。
- letter_line：推薦信中間那一句。HR 只花兩秒看，所以要讓人想打開履歷：點出這個職缺最在意的一件事，配上使用者最相關的一個成果或數字；優先用 <profile> 推薦信那一節的說法。不要列技術名詞或實作細節。只能用 <profile> 裡寫的事實，不可誇大或編造，40 字以內。
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
			"required_skills": map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"weight": map[string]any{"type": "string", "enum": skillWeights},
					"has":    map[string]any{"type": "string", "enum": skillHas},
				},
				"required": []string{"name", "weight", "has"},
			}},
			"jd_detail":    map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
			"to_confirm":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 3},
			"company_type": map[string]any{"type": "string", "enum": companyTypes},
			"salary":       map[string]any{"type": "string"},
			"red_flags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 3},
			"evidence":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 4},
			"resume":       map[string]any{"type": "string", "enum": resumes},
			"add_ml_line":  map[string]any{"type": "boolean"},
			"letter_line":  map[string]any{"type": "string"},
			"reason":       map[string]any{"type": "string"},
		},
		Required: []string{"job_title", "company", "score", "verdict", "direction_fit", "skills_fit",
			"required_skills", "jd_detail", "company_type", "salary", "red_flags", "to_confirm", "evidence",
			"resume", "add_ml_line", "letter_line", "reason"},
	},
}}
