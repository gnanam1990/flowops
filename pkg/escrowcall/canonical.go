package escrowcall

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

var errNonCanonicalValue = errors.New("value is outside the escrow-call JCS profile")

var maxSafeJSONInteger = big.NewInt(9_007_199_254_740_991)

// canonicalJSON implements the RFC 8785 rules needed by escrow-call/1. This
// profile deliberately permits only I-JSON strings, booleans, null, arrays,
// objects, and non-negative safe integers; all monetary uint256 values remain
// decimal strings on the wire.
func canonicalJSON(value interface{}) ([]byte, error) {
	if err := validateCanonicalInput(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded interface{}
	if err := decoder.Decode(&decoded); err != nil || requireEOF(decoder) != nil {
		return nil, errNonCanonicalValue
	}
	var output bytes.Buffer
	if err := writeCanonical(&output, decoded); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validateCanonicalInput(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > 128 {
		return errNonCanonicalValue
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return validateCanonicalInput(value.Elem(), depth+1)
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errNonCanonicalValue
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				if err := validateCanonicalInput(value.Field(index), depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return errNonCanonicalValue
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateCanonicalInput(iterator.Key(), depth+1); err != nil {
				return err
			}
			if err := validateCanonicalInput(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateCanonicalInput(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCanonical(output *bytes.Buffer, value interface{}) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case string:
		if err := writeCanonicalString(output, typed); err != nil {
			return err
		}
	case json.Number:
		if !regexpCanonicalInteger(typed.String()) {
			return errNonCanonicalValue
		}
		integer, ok := new(big.Int).SetString(typed.String(), 10)
		if !ok || integer.Sign() < 0 || integer.Cmp(maxSafeJSONInteger) > 0 {
			return errNonCanonicalValue
		}
		output.WriteString(typed.String())
	case []interface{}:
		output.WriteByte('[')
		for index, item := range typed {
			if index != 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if !utf8.ValidString(key) {
				return errNonCanonicalValue
			}
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool { return utf16Less(keys[left], keys[right]) })
		output.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, key); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := writeCanonical(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return errNonCanonicalValue
	}
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return errNonCanonicalValue
	}
	const hex = "0123456789abcdef"
	output.WriteByte('"')
	for _, character := range []byte(value) {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteByte(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				output.WriteString(`\u00`)
				output.WriteByte(hex[character>>4])
				output.WriteByte(hex[character&0x0f])
			} else {
				output.WriteByte(character)
			}
		}
	}
	output.WriteByte('"')
	return nil
}

func regexpCanonicalInteger(value string) bool {
	if value == "0" {
		return true
	}
	if len(value) == 0 || value[0] < '1' || value[0] > '9' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func utf16Less(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := len(leftUnits)
	if len(rightUnits) < limit {
		limit = len(rightUnits)
	}
	for index := 0; index < limit; index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}
