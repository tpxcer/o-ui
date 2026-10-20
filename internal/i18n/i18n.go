// Package i18n 提供简体中文与英文两种文案。源码处成对登记，中文为默认值；
// 接口边界按请求语言把已登记的中文替换为英文，未登记的文本保持原样。
package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

const Header = "X-O-UI-Lang"

var english sync.Map

// T登记一对文案并返回中文。
func T(zh, en string) string {
	english.Store(zh, en)
	return zh
}

// New返回中文错误并登记英文，errors.Is比较不受影响。
func New(zh, en string) error { return errors.New(T(zh, en)) }

// Errorf按相同参数分别格式化两种语言；不支持%w包装。
func Errorf(zh, en string, args ...any) error {
	return errors.New(T(fmt.Sprintf(zh, args...), fmt.Sprintf(en, args...)))
}

type wrapped struct {
	message string
	err     error
}

func (w *wrapped) Error() string { return w.message }
func (w *wrapped) Unwrap() error { return w.err }

// Wrap给已有错误加双语前缀并保留errors.Is/As链；内层已登记时英文一并翻译。
func Wrap(zh, en string, err error) error {
	inner := err.Error()
	return &wrapped{T(zh+inner, en+Translate(inner, "en")), err}
}

func Translate(text, lang string) string {
	if lang != "en" {
		return text
	}
	if value, ok := english.Load(text); ok {
		return value.(string)
	}
	return text
}

// RequestLang只认显式请求头en，其余（含缺省）为中文，保持既有行为。
func RequestLang(r *http.Request) string {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get(Header)), "en") {
		return "en"
	}
	return "zh"
}

// EnvLang按LC_ALL、LC_MESSAGES、LANG的优先顺序选择命令行语言，zh开头为中文，其余为英文。
func EnvLang() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			if strings.HasPrefix(strings.ToLower(value), "zh") {
				return "zh"
			}
			return "en"
		}
	}
	return "en"
}

func Pick(lang, zh, en string) string {
	if lang == "en" {
		return en
	}
	return zh
}

// TranslateJSON把可序列化值中已登记的中文字符串换成英文；用于接口响应边界。
func TranslateJSON(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err = decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return walk(decoded), nil
}

func walk(value any) any {
	switch v := value.(type) {
	case string:
		return Translate(v, "en")
	case []any:
		for i := range v {
			v[i] = walk(v[i])
		}
	case map[string]any:
		for key, item := range v {
			v[key] = walk(item)
		}
	}
	return value
}
