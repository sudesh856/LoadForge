

package assertions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	TypeStatus       = "status"        
	TypeBodyContains = "body_contains" 
	TypeHeader       = "header"        
	TypeJSONField    = "json_field"    
)

type Assertion struct {
	Type     string `yaml:"type"`     
	Key      string `yaml:"key"`      
	Path     string `yaml:"path"`     
	Value    string `yaml:"value"`    
	Equals   string `yaml:"equals"`   
	Contains string `yaml:"contains"` 
}

type Failure struct {
	EndpointName string
	AssertionType string
	Message      string
}

func (f Failure) Error() string {
	return fmt.Sprintf("ASSERTION FAILED [%s] %s: %s", f.EndpointName, f.AssertionType, f.Message)
}


func Run(endpointName string, assertions []Assertion, resp *http.Response, body []byte) []Failure {
	var failures []Failure

	for _, a := range assertions {
		switch a.Type {
		case TypeStatus:
			if f := checkStatus(endpointName, a, resp); f != nil {
				failures = append(failures, *f)
			}
		case TypeBodyContains:
			if f := checkBodyContains(endpointName, a, body); f != nil {
				failures = append(failures, *f)
			}
		case TypeHeader:
			if f := checkHeader(endpointName, a, resp); f != nil {
				failures = append(failures, *f)
			}
		case TypeJSONField:
			if f := checkJSONField(endpointName, a, body); f != nil {
				failures = append(failures, *f)
			}
		default:
			failures = append(failures, Failure{
				EndpointName:  endpointName,
				AssertionType: a.Type,
				Message:       fmt.Sprintf("unknown assertion type %q", a.Type),
			})
		}
	}

	return failures
}


func checkStatus(name string, a Assertion, resp *http.Response) *Failure {
	want := a.Value
	if want == "" {
		want = a.Equals
	}
	if want == "" {
		return nil 
	}

	wantCode, err := strconv.Atoi(want)
	if err != nil {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeStatus,
			Message:       fmt.Sprintf("invalid status value %q (must be a number)", want),
		}
	}

	if resp.StatusCode != wantCode {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeStatus,
			Message:       fmt.Sprintf("expected %d got %d", wantCode, resp.StatusCode),
		}
	}
	return nil
}

func checkBodyContains(name string, a Assertion, body []byte) *Failure {
	needle := a.Value
	if needle == "" {
		needle = a.Contains
	}
	if needle == "" {
		return nil
	}

	if !strings.Contains(string(body), needle) {
		preview := string(body)
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeBodyContains,
			Message:       fmt.Sprintf("body does not contain %q (body: %s)", needle, preview),
		}
	}
	return nil
}

func checkHeader(name string, a Assertion, resp *http.Response) *Failure {
	if a.Key == "" {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeHeader,
			Message:       "header assertion missing `key`",
		}
	}

	actual := resp.Header.Get(a.Key)

	if a.Equals != "" && !strings.EqualFold(actual, a.Equals) {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeHeader,
			Message:       fmt.Sprintf("header %q: expected %q got %q", a.Key, a.Equals, actual),
		}
	}

	if a.Contains != "" && !strings.Contains(strings.ToLower(actual), strings.ToLower(a.Contains)) {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeHeader,
			Message:       fmt.Sprintf("header %q: %q does not contain %q", a.Key, actual, a.Contains),
		}
	}

	return nil
}

func checkJSONField(name string, a Assertion, body []byte) *Failure {
	if a.Path == "" {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeJSONField,
			Message:       "json_field assertion missing `path`",
		}
	}

	var root interface{}
	if err := json.Unmarshal(body, &root); err != nil {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeJSONField,
			Message:       fmt.Sprintf("response is not valid JSON: %v", err),
		}
	}

	actual, err := walkPath(root, strings.Split(a.Path, "."))
	if err != nil {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeJSONField,
			Message:       fmt.Sprintf("path %q not found: %v", a.Path, err),
		}
	}

	actualStr := fmt.Sprintf("%v", actual)

	want := a.Value
	if want == "" {
		want = a.Equals
	}

	if want != "" && actualStr != want {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeJSONField,
			Message:       fmt.Sprintf("json path %q: expected %q got %q", a.Path, want, actualStr),
		}
	}

	if a.Contains != "" && !strings.Contains(actualStr, a.Contains) {
		return &Failure{
			EndpointName:  name,
			AssertionType: TypeJSONField,
			Message:       fmt.Sprintf("json path %q: %q does not contain %q", a.Path, actualStr, a.Contains),
		}
	}

	return nil
}

func walkPath(node interface{}, parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return node, nil
	}

	key := parts[0]
	rest := parts[1:]

	switch n := node.(type) {
	case map[string]interface{}:
		val, ok := n[key]
		if !ok {
			return nil, fmt.Errorf("key %q not found", key)
		}
		return walkPath(val, rest)
	case []interface{}:
		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("expected array index, got %q", key)
		}
		if idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("index %d out of range (len=%d)", idx, len(n))
		}
		return walkPath(n[idx], rest)
	default:
		return nil, fmt.Errorf("cannot traverse %T with key %q", node, key)
	}
}