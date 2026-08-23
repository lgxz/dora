package progress

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lgxz/dora"
	"github.com/rivo/uniseg"
)

const (
	maxOperationRunes = 72
	maxResultRunes    = 48
	maxErrorRunes     = 96
)

type outcome uint8

const (
	outcomeSuccess outcome = iota
	outcomeWarning
	outcomeFailure
)

type toolPresentation struct {
	name    string
	summary string
	result  string
	outcome outcome
}

type toolFormatter struct {
	call   func(json.RawMessage) string
	result func(dora.ToolCall, dora.Message) (string, outcome)
}

var toolFormatters = map[string]toolFormatter{
	"bash":       {call: formatCommandCall, result: formatCommandResult},
	"powershell": {call: formatCommandCall, result: formatCommandResult},
	"read":       {call: formatReadCall, result: formatReadResult},
	"write":      {call: formatWriteCall, result: formatWriteResult},
	"edit":       {call: formatEditCall, result: formatEditResult},
	"grep":       {call: formatGrepCall, result: formatGrepResult},
	"glob":       {call: formatGlobCall, result: formatGlobResult},
	"skill":      {call: formatSkillCall, result: staticResult("loaded")},
	"history":    {call: formatHistoryCall, result: formatHistoryResult},
	"job":        {call: formatJobCall, result: formatJobResult},
	"task":       {call: formatTaskCall, result: formatTaskResult},
}

func presentTool(call dora.ToolCall, message dora.Message) toolPresentation {
	formatter, known := toolFormatters[call.Name]
	presentation := toolPresentation{name: call.Name, outcome: outcomeSuccess}
	if known {
		presentation.summary = formatter.call(call.Input)
		presentation.result, presentation.outcome = formatter.result(call, message)
	} else {
		presentation.summary = genericArguments(call.Input)
		presentation.result = "done"
	}
	if call.Name == "bash" || call.Name == "powershell" {
		presentation.name = ""
	}
	presentation.summary = truncateLine(presentation.summary, maxOperationRunes)
	presentation.result = truncateLine(presentation.result, maxResultRunes)
	return presentation
}

func presentToolFailure(call dora.ToolCall, err error) toolPresentation {
	formatter, known := toolFormatters[call.Name]
	summary := genericArguments(call.Input)
	if known {
		summary = formatter.call(call.Input)
	}
	name := call.Name
	if name == "bash" || name == "powershell" {
		name = ""
	}
	result := "failed"
	if err != nil {
		result = cleanToolError(call.Name, err.Error())
	}
	return toolPresentation{
		name:    name,
		summary: truncateLine(summary, maxOperationRunes),
		result:  truncateLine(result, maxErrorRunes),
		outcome: outcomeFailure,
	}
}

func formatCommandCall(raw json.RawMessage) string {
	var input struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &input) == nil && input.Command != "" {
		return collapse(input.Command)
	}
	return genericArguments(raw)
}

func formatTaskCall(raw json.RawMessage) string {
	var input struct {
		Instruction string `json:"instruction"`
	}
	if json.Unmarshal(raw, &input) == nil && input.Instruction != "" {
		return collapse(input.Instruction)
	}
	return genericArguments(raw)
}

func formatTaskResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	var result struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(message.Content), &result) == nil && result.JobID != "" && result.Status == "running" {
		return "background " + result.JobID, outcomeWarning
	}
	return "done", outcomeSuccess
}

func formatCommandResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	var result struct {
		ExitCode  *int   `json:"exit_code"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		TimedOut  bool   `json:"timed_out"`
		Truncated bool   `json:"truncated"`
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	if json.Unmarshal([]byte(message.Content), &result) != nil {
		return "done", outcomeSuccess
	}
	if result.Error != "" {
		return collapse(result.Error), outcomeFailure
	}
	outputSize := formatStreamSizes(len(result.Stdout), len(result.Stderr))
	if result.TimedOut {
		return joinResult("timed out", outputSize), outcomeFailure
	}
	if result.JobID != "" && result.Status == "running" {
		return joinResult("background job "+result.JobID, outputSize), outcomeWarning
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		return outputSize, outcomeFailure
	}
	if result.Truncated {
		return joinResult(outputSize, "output truncated"), outcomeWarning
	}
	if result.ExitCode == nil {
		return "done", outcomeSuccess
	}
	return outputSize, outcomeSuccess
}

func formatReadCall(raw json.RawMessage) string {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Path == "" {
		return genericArguments(raw)
	}
	if input.Offset == 0 && input.Limit == nil {
		return input.Path
	}
	start := input.Offset
	if start == 0 {
		start = 1
	}
	limit := 200
	if input.Limit != nil {
		limit = *input.Limit
	}
	return fmt.Sprintf("%s:%d-%d", input.Path, start, start+limit-1)
}

func formatReadResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	content := strings.TrimSpace(message.Content)
	switch {
	case content == "", content == "(empty file)":
		return "empty", outcomeSuccess
	case strings.HasPrefix(content, "(binary file;"):
		return "binary file", outcomeWarning
	case strings.HasPrefix(content, "(offset "):
		return strings.Trim(content, "()"), outcomeWarning
	default:
		return plural(lineCount(message.Content), "line", "lines"), outcomeSuccess
	}
}

func formatWriteCall(raw json.RawMessage) string {
	var input struct {
		Path   string `json:"path"`
		Append bool   `json:"append"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Path == "" {
		return genericArguments(raw)
	}
	if input.Append {
		return "append " + input.Path
	}
	return input.Path
}

func formatWriteResult(call dora.ToolCall, message dora.Message) (string, outcome) {
	var bytesWritten int
	var created string
	if _, err := fmt.Sscanf(message.Content, "bytes_written: %d, created: %s", &bytesWritten, &created); err != nil {
		return "done", outcomeSuccess
	}
	action := "updated"
	if created == "true" {
		action = "created"
	} else {
		var input struct {
			Append bool `json:"append"`
		}
		if json.Unmarshal(call.Input, &input) == nil && input.Append {
			action = "appended"
		}
	}
	return fmt.Sprintf("%s, %s", action, formatBytes(bytesWritten)), outcomeSuccess
}

func formatEditCall(raw json.RawMessage) string {
	var input struct {
		Path       string `json:"path"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Path == "" {
		return genericArguments(raw)
	}
	if input.ReplaceAll {
		return input.Path + " (all)"
	}
	return input.Path
}

func formatEditResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	if strings.TrimSpace(message.Content) == "old_string not found in file" {
		return "old_string not found", outcomeWarning
	}
	var replacements, changed int
	if _, err := fmt.Sscanf(message.Content, "replacements: %d, bytes_changed: %d", &replacements, &changed); err != nil {
		return "done", outcomeSuccess
	}
	return fmt.Sprintf("%s, %s B", plural(replacements, "replacement", "replacements"), signed(changed)), outcomeSuccess
}

func formatGrepCall(raw json.RawMessage) string {
	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Pattern == "" || input.Path == "" {
		return genericArguments(raw)
	}
	return fmt.Sprintf("%s in %s", strconv.Quote(input.Pattern), input.Path)
}

func formatGrepResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	if strings.TrimSpace(message.Content) == "(no matches)" {
		return "no matches", outcomeWarning
	}
	return plural(lineCount(message.Content), "match", "matches"), outcomeSuccess
}

func formatGlobCall(raw json.RawMessage) string {
	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Pattern == "" {
		return genericArguments(raw)
	}
	if input.Path == "" {
		input.Path = "."
	}
	return fmt.Sprintf("%s in %s", strconv.Quote(input.Pattern), input.Path)
}

func formatGlobResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	if strings.TrimSpace(message.Content) == "(no matches)" {
		return "no matches", outcomeWarning
	}
	return plural(lineCount(message.Content), "file", "files"), outcomeSuccess
}

func formatSkillCall(raw json.RawMessage) string {
	var input struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &input) == nil && input.Name != "" {
		return input.Name
	}
	return genericArguments(raw)
}

func formatHistoryCall(raw json.RawMessage) string {
	var input struct {
		Action string `json:"action"`
		TurnID *int64 `json:"turn_id"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Action == "" {
		return genericArguments(raw)
	}
	if input.Action == "get" && input.TurnID != nil {
		return fmt.Sprintf("get turn %d", *input.TurnID)
	}
	return input.Action
}

func formatHistoryResult(call dora.ToolCall, message dora.Message) (string, outcome) {
	var input struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(call.Input, &input)
	var page struct {
		Total  int               `json:"total"`
		Turns  []json.RawMessage `json:"turns"`
		Rounds []json.RawMessage `json:"rounds"`
	}
	if json.Unmarshal([]byte(message.Content), &page) != nil {
		return "done", outcomeSuccess
	}
	if input.Action == "get" {
		return fmt.Sprintf("%d/%d rounds", len(page.Rounds), page.Total), outcomeSuccess
	}
	return fmt.Sprintf("%d/%d turns", len(page.Turns), page.Total), outcomeSuccess
}

func formatJobCall(raw json.RawMessage) string {
	var input struct {
		Action string `json:"action"`
		JobID  string `json:"job_id"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Action == "" {
		return genericArguments(raw)
	}
	if input.JobID == "" {
		return input.Action
	}
	return input.Action + " " + input.JobID
}

func formatJobResult(_ dora.ToolCall, message dora.Message) (string, outcome) {
	var result struct {
		Error    string            `json:"error"`
		Status   string            `json:"status"`
		ExitCode *int              `json:"exit_code"`
		Jobs     []json.RawMessage `json:"jobs"`
	}
	if json.Unmarshal([]byte(message.Content), &result) != nil {
		return "done", outcomeSuccess
	}
	if result.Error != "" {
		return collapse(result.Error), outcomeFailure
	}
	if result.Jobs != nil {
		return plural(len(result.Jobs), "job", "jobs"), outcomeSuccess
	}
	text := result.Status
	if text == "" {
		text = "done"
	}
	if result.ExitCode != nil {
		text = joinResult(text, fmt.Sprintf("exit %d", *result.ExitCode))
	}
	switch result.Status {
	case "running", "cancelling":
		return text, outcomeWarning
	case "failed", "timed_out":
		return text, outcomeFailure
	default:
		return text, outcomeSuccess
	}
}

func staticResult(text string) func(dora.ToolCall, dora.Message) (string, outcome) {
	return func(dora.ToolCall, dora.Message) (string, outcome) {
		return text, outcomeSuccess
	}
}

func genericArguments(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return "arguments unavailable"
	}
	priority := []string{"action", "name", "path", "pattern", "query", "id", "job_id", "turn_id"}
	keys := make([]string, 0, len(object))
	seen := make(map[string]bool, len(object))
	for _, key := range priority {
		if _, ok := object[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range object {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	parts := make([]string, 0, 2)
	for _, key := range keys {
		value, ok := safeScalar(key, object[key])
		if !ok {
			continue
		}
		parts = append(parts, key+"="+value)
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func safeScalar(key string, raw json.RawMessage) (string, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, fragment := range []string{"token", "secret", "password", "passwd", "api_key", "authorization", "cookie"} {
		if strings.Contains(normalized, fragment) {
			return "<redacted>", true
		}
	}
	for _, payload := range []string{"content", "body", "text", "old_string", "new_string"} {
		if normalized == payload {
			return "<omitted>", true
		}
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strconv.Quote(truncateLine(typed, 32)), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	case nil:
		return "null", true
	default:
		return "", false
	}
}

func cleanToolError(name, message string) string {
	message = collapse(message)
	prefix := fmt.Sprintf("execute tool %q: ", name)
	message = strings.TrimPrefix(message, prefix)
	if strings.Contains(message, "arguments are not valid JSON") {
		return "invalid JSON arguments"
	}
	return message
}

func collapse(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateLine(value string, limit int) string {
	value = collapse(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func fitDisplayWidth(value string, width int) string {
	value = collapse(value)
	if width <= 0 {
		return ""
	}
	if uniseg.StringWidth(value) <= width {
		return value + strings.Repeat(" ", width-uniseg.StringWidth(value))
	}

	var fitted strings.Builder
	used := 0
	clusters := uniseg.NewGraphemes(value)
	for clusters.Next() {
		cluster := clusters.Str()
		clusterWidth := uniseg.StringWidth(cluster)
		if used+clusterWidth+1 > width {
			break
		}
		fitted.WriteString(cluster)
		used += clusterWidth
	}
	fitted.WriteRune('…')
	used++
	if used < width {
		fitted.WriteString(strings.Repeat(" ", width-used))
	}
	return fitted.String()
}

func lineCount(value string) int {
	value = strings.TrimRight(value, "\r\n")
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func plural(count int, singular, plural string) string {
	word := plural
	if count == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", count, word)
}

func joinResult(parts ...string) string {
	var present []string
	for _, part := range parts {
		if part != "" {
			present = append(present, part)
		}
	}
	return strings.Join(present, ", ")
}

func signed(value int) string {
	if value > 0 {
		return "+" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func formatBytes(value int) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	if value < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(value)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(value)/(1024*1024))
}

func formatStreamSizes(stdout, stderr int) string {
	if stdout == 0 && stderr == 0 {
		return ""
	}
	return formatCompactBytes(stdout) + "/" + formatCompactBytes(stderr)
}

func formatCompactBytes(value int) string {
	const (
		kilobyte = 1024
		megabyte = 1024 * 1024
	)
	if value < kilobyte {
		return fmt.Sprintf("%dB", value)
	}
	if value < megabyte {
		return formatCompactUnit(value, kilobyte, "K")
	}
	return formatCompactUnit(value, megabyte, "M")
}

func formatCompactUnit(value, unit int, suffix string) string {
	if value%unit == 0 {
		return fmt.Sprintf("%d%s", value/unit, suffix)
	}
	return fmt.Sprintf("%.1f%s", float64(value)/float64(unit), suffix)
}
