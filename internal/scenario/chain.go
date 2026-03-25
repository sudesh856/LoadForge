package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ChainStore struct {
	data map[string]string
}

func NewChainStore() *ChainStore {
	return &ChainStore{data: make(map[string]string)}
}

func (c *ChainStore) Store(endpointName string, body []byte, extract map[string]string) {
	if len(extract) == 0{
		return
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}

	for varName, jsonKey := range extract {
		key := strings.TrimPrefix(jsonKey, "$.")
		val := getNestedValue(parsed, key)
		if val != "" {
			c.data[endpointName+"."+varName] = val
		}
	}
}

func (c *ChainStore) Get(endpointName, varName string) (string, bool) {
	val, ok := c.data[endpointName+"."+varName]
	return val, ok
}

func (c *ChainStore) ToVars() map[string]string {
	out := make(map[string]string)
	for k,v := range c.data {
		out[k] = v
	}
	return out
}


func getNestedValue(data map[string]interface{}, path string) string {
    parts := strings.Split(path, ".")
    current := data

    for i, part := range parts {
        val, ok := current[part]
        if !ok {
            return ""
        }
        // if last part, return the value
        if i == len(parts)-1 {
            return fmt.Sprintf("%v", val)
        }
        // otherwise go deeper
        nested, ok := val.(map[string]interface{})
        if !ok {
            return ""
        }
        current = nested
    }
    return ""
}
