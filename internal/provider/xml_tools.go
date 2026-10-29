package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	toolCallTagRe       = regexp.MustCompile(`(?s)<tool_call(?:\s+(?:name|tool)=["']?([a-zA-Z0-9_\-\.]+)["']?)?\s*>(.*?)</tool_call>`)
	markdownToolCallRe  = regexp.MustCompile("(?s)```(?:xml|tool_call|json)?\\s*(<tool_call.*?</tool_call>)\\s*```")
	markdownBlockCallRe = regexp.MustCompile("(?s)```(?:tool_call)\\s*\n?({.*?})\n?\\s*```")
	functionTagRe       = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-\.]+)\s*>(.*?)</function>`)
	argKeyValueRe       = regexp.MustCompile(`(?s)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
	thinkTagRe          = regexp.MustCompile(`(?s)<think(?:ing)?>(.*?)</think(?:ing)?>`)
	reTrailingComma     = regexp.MustCompile(`,(\s*[}\]])`)
)

// SanitizeJSONArguments cleans common minor syntax defects from model-emitted
// JSON arguments, such as trailing commas, markdown fencing, or unescaped wrappers.
func SanitizeJSONArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var dummy any
	if json.Unmarshal([]byte(trimmed), &dummy) == nil {
		return trimmed
	}
	// Strip markdown codeblock if arguments string is wrapped in ```json ... ```
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") {
			stripped := strings.Join(lines[1:], "\n")
			if idx := strings.LastIndex(stripped, "```"); idx != -1 {
				stripped = strings.TrimSpace(stripped[:idx])
			}
			if json.Unmarshal([]byte(stripped), &dummy) == nil {
				return stripped
			}
			trimmed = stripped
		}
	}
	// Fix trailing commas before closing braces or brackets (common model quirk)
	fixed := reTrailingComma.ReplaceAllString(trimmed, "$1")
	if json.Unmarshal([]byte(fixed), &dummy) == nil {
		return fixed
	}
	return trimmed
}

// ExtractEmbeddedReasoning pulls <think>...</think> and <thinking>...</thinking>
// chain-of-thought blocks out of the main message content so internal model
// deliberation is captured in Reasoning rather than leaking into visible user-facing output.
func ExtractEmbeddedReasoning(content string) (string, string) {
	if !strings.Contains(content, "<think>") && !strings.Contains(content, "<thinking>") {
		return "", content
	}

	var reasoningParts []string
	cleaned := thinkTagRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := thinkTagRe.FindStringSubmatch(match)
		if len(sub) >= 2 {
			r := strings.TrimSpace(sub[1])
			if r != "" {
				reasoningParts = append(reasoningParts, r)
			}
		}
		return ""
	})

	reasoning := strings.Join(reasoningParts, "\n\n")
	return reasoning, strings.TrimSpace(cleaned)
}

// ExtractEmbeddedToolCalls inspects message content for pseudo-XML tool calls
// emitted by models like Poolside laguna-s-2.1, Qwen, or custom fine-tunes.
// It parses the tool calls into standard ToolCall structs and returns the
// cleaned remaining text content.
func ExtractEmbeddedToolCalls(content string) ([]ToolCall, string) {
	if !strings.Contains(content, "<tool_call") && !strings.Contains(content, "<function=") && !strings.Contains(content, "```tool_call") {
		return nil, content
	}

	var calls []ToolCall
	cleaned := content

	// Unwrap markdown fences around XML tool calls if present (e.g. ```xml\n<tool_call>...</tool_call>\n```)
	cleaned = markdownToolCallRe.ReplaceAllString(cleaned, "$1")

	// Match raw ```tool_call\n{"name":"...","arguments":{...}}\n``` blocks
	cleaned = markdownBlockCallRe.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := markdownBlockCallRe.FindStringSubmatch(match)
		if len(sub) >= 2 {
			rawJSON := strings.TrimSpace(sub[1])
			var obj map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &obj); err == nil {
				name, _ := obj["name"].(string)
				if name != "" {
					var argsStr string
					if argsObj, ok := obj["arguments"]; ok {
						switch a := argsObj.(type) {
						case string:
							argsStr = a
						default:
							b, _ := json.Marshal(a)
							argsStr = string(b)
						}
					} else {
						delete(obj, "name")
						b, _ := json.Marshal(obj)
						argsStr = string(b)
					}
					calls = append(calls, ToolCall{
						ID:        fmt.Sprintf("call_block_%d_%d", time.Now().UnixNano(), len(calls)+1),
						Name:      name,
						Arguments: SanitizeJSONArguments(argsStr),
					})
					return ""
				}
			}
		}
		return match
	})

	// Match <tool_call>...</tool_call> with optional name attribute: <tool_call name="...">...</tool_call>
	cleaned = toolCallTagRe.ReplaceAllStringFunc(cleaned, func(match string) string {
		inner := toolCallTagRe.FindStringSubmatch(match)
		if len(inner) < 3 {
			return match
		}
		attrName := strings.TrimSpace(inner[1])
		raw := strings.TrimSpace(inner[2])

		// Case A: Attribute format <tool_call name="xyz">arguments</tool_call>
		if attrName != "" {
			// Check if inner content has Poolside <arg_key> tags
			if strings.Contains(raw, "<arg_key>") {
				argsMap := make(map[string]any)
				matches := argKeyValueRe.FindAllStringSubmatch(raw, -1)
				for _, m := range matches {
					if len(m) >= 3 {
						k := strings.TrimSpace(m[1])
						v := m[2]
						argsMap[k] = v
					}
				}
				argsBytes, _ := json.Marshal(argsMap)
				calls = append(calls, ToolCall{
					ID:        fmt.Sprintf("call_xml_%d_%d", time.Now().UnixNano(), len(calls)+1),
					Name:      attrName,
					Arguments: string(argsBytes),
				})
				return ""
			}

			// Otherwise raw is directly the JSON arguments
			calls = append(calls, ToolCall{
				ID:        fmt.Sprintf("call_xml_%d_%d", time.Now().UnixNano(), len(calls)+1),
				Name:      attrName,
				Arguments: SanitizeJSONArguments(raw),
			})
			return ""
		}

		// Case B: Poolside format <tool_call>name<arg_key>k</arg_key><arg_value>v</arg_value>...</tool_call>
		if strings.Contains(raw, "<arg_key>") {
			firstKeyIdx := strings.Index(raw, "<arg_key>")
			toolName := strings.TrimSpace(raw[:firstKeyIdx])
			argsMap := make(map[string]any)

			matches := argKeyValueRe.FindAllStringSubmatch(raw, -1)
			for _, m := range matches {
				if len(m) >= 3 {
					k := strings.TrimSpace(m[1])
					v := m[2] // preserve whitespace inside arg_value
					argsMap[k] = v
				}
			}

			if toolName != "" && len(argsMap) > 0 {
				argsBytes, _ := json.Marshal(argsMap)
				calls = append(calls, ToolCall{
					ID:        fmt.Sprintf("call_xml_%d_%d", time.Now().UnixNano(), len(calls)+1),
					Name:      toolName,
					Arguments: string(argsBytes),
				})
				return "" // strip XML tag from visible text
			}
		}

		// Case C: JSON object inside <tool_call>...</tool_call>
		var obj map[string]any
		if err := json.Unmarshal([]byte(SanitizeJSONArguments(raw)), &obj); err == nil {
			name, _ := obj["name"].(string)
			if name != "" {
				var argsStr string
				if argsObj, ok := obj["arguments"]; ok {
					switch a := argsObj.(type) {
					case string:
						argsStr = a
					default:
						b, _ := json.Marshal(a)
						argsStr = string(b)
					}
				} else {
					delete(obj, "name")
					b, _ := json.Marshal(obj)
					argsStr = string(b)
				}
				calls = append(calls, ToolCall{
					ID:        fmt.Sprintf("call_xml_%d_%d", time.Now().UnixNano(), len(calls)+1),
					Name:      name,
					Arguments: SanitizeJSONArguments(argsStr),
				})
				return ""
			}
		}

		// Case D: tool_name\n{json_arguments}
		lines := strings.SplitN(raw, "\n", 2)
		if len(lines) == 2 {
			toolName := strings.TrimSpace(lines[0])
			jsonArgs := strings.TrimSpace(lines[1])
			sanitizedArgs := SanitizeJSONArguments(jsonArgs)
			var testObj map[string]any
			if toolName != "" && json.Unmarshal([]byte(sanitizedArgs), &testObj) == nil {
				calls = append(calls, ToolCall{
					ID:        fmt.Sprintf("call_xml_%d_%d", time.Now().UnixNano(), len(calls)+1),
					Name:      toolName,
					Arguments: sanitizedArgs,
				})
				return ""
			}
		}

		return match
	})

	// 2. Match <function=name>arguments</function>
	cleaned = functionTagRe.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := functionTagRe.FindStringSubmatch(match)
		if len(sub) >= 3 {
			toolName := strings.TrimSpace(sub[1])
			argsStr := strings.TrimSpace(sub[2])
			if toolName != "" {
				calls = append(calls, ToolCall{
					ID:        fmt.Sprintf("call_fn_%d_%d", time.Now().UnixNano(), len(calls)+1),
					Name:      toolName,
					Arguments: SanitizeJSONArguments(argsStr),
				})
				return ""
			}
		}
		return match
	})

	cleaned = strings.TrimSpace(cleaned)
	return calls, cleaned
}
