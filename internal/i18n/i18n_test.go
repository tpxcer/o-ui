package i18n

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestBilingualMessages(t *testing.T) {
	err := New("测试中文", "test english")
	if err.Error() != "测试中文" || Translate(err.Error(), "en") != "test english" || Translate(err.Error(), "zh") != "测试中文" {
		t.Fatal(err, Translate(err.Error(), "en"))
	}
	if Translate("未登记", "en") != "未登记" {
		t.Fatal("未登记文案应保持原样")
	}
	wrapped := Wrap("前缀：", "Prefix: ", err)
	if !errors.Is(wrapped, err) || wrapped.Error() != "前缀：测试中文" || Translate(wrapped.Error(), "en") != "Prefix: test english" {
		t.Fatal(wrapped, Translate(wrapped.Error(), "en"))
	}
	formatted := Errorf("端口%d", "port %d", 8443)
	if formatted.Error() != "端口8443" || Translate(formatted.Error(), "en") != "port 8443" {
		t.Fatal(formatted)
	}
	value, err := TranslateJSON(map[string]any{"error": "测试中文", "n": uint64(1) << 60, "list": []string{"前缀：测试中文", "user data"}})
	if err != nil {
		t.Fatal(err)
	}
	m := value.(map[string]any)
	if m["error"] != "test english" || m["n"].(interface{ String() string }).String() != "1152921504606846976" || m["list"].([]any)[0] != "Prefix: test english" || m["list"].([]any)[1] != "user data" {
		t.Fatal(m)
	}
}

func TestRequestAndEnvironmentLanguage(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if RequestLang(r) != "zh" {
		t.Fatal("缺省应为中文")
	}
	r.Header.Set(Header, " EN ")
	if RequestLang(r) != "en" {
		t.Fatal("显式en应为英文")
	}
	for env, want := range map[[3]string]string{
		{"", "", ""}: "en", {"", "", "zh_CN.UTF-8"}: "zh", {"", "", "en_US.UTF-8"}: "en",
		{"C", "", "zh_CN.UTF-8"}: "en", {"zh_TW.UTF-8", "", "en_US.UTF-8"}: "zh", {"", "zh_CN", "C"}: "zh",
	} {
		t.Setenv("LC_ALL", env[0])
		t.Setenv("LC_MESSAGES", env[1])
		t.Setenv("LANG", env[2])
		if got := EnvLang(); got != want {
			t.Fatal(env, got)
		}
	}
}

// 防止新增产品中文文案遗漏英文：非测试Go源码里的中文字符串字面量必须作为i18n调用的参数，
// 仅允许与请求比较的确认口令和const。
func TestGoMessagesAreBilingual(t *testing.T) {
	root, _ := filepath.Abs("../..")
	var missing []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			if d != nil && d.IsDir() && path != root {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/internal/i18n/") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		allowed := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if s, ok := v.Fun.(*ast.SelectorExpr); ok {
					if x, ok := s.X.(*ast.Ident); ok && (x.Name == "i18n" || x.Name == "say") || s.Sel.Name == "say" {
						for _, arg := range v.Args {
							ast.Inspect(arg, func(n ast.Node) bool {
								if lit, ok := n.(*ast.BasicLit); ok {
									allowed[lit] = true
								}
								return true
							})
						}
					}
				}
				if x, ok := v.Fun.(*ast.Ident); ok && x.Name == "say" {
					for _, arg := range v.Args {
						if lit, ok := arg.(*ast.BasicLit); ok {
							allowed[lit] = true
						}
					}
				}
			case *ast.BinaryExpr:
				if v.Op == token.EQL || v.Op == token.NEQ {
					for _, side := range []ast.Expr{v.X, v.Y} {
						if lit, ok := side.(*ast.BasicLit); ok {
							allowed[lit] = true
						}
					}
				}
			case *ast.GenDecl:
				if v.Tok == token.CONST {
					ast.Inspect(v, func(n ast.Node) bool {
						if lit, ok := n.(*ast.BasicLit); ok {
							allowed[lit] = true
						}
						return true
					})
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || allowed[lit] {
				return true
			}
			value, _ := strconv.Unquote(lit.Value)
			for _, r := range value {
				if unicode.Is(unicode.Han, r) {
					rel, _ := filepath.Rel(root, path)
					missing = append(missing, rel+": "+value)
					break
				}
			}
			return true
		})
		return nil
	})
	if len(missing) > 0 {
		t.Fatal("中文字符串缺少英文配对：\n" + strings.Join(missing, "\n"))
	}
}
