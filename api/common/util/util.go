package util

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
)

// e.g. 0x9536de7dab6e2942965234edbe20450
func HexadecimalStringToBigInt(hexString string) (*big.Int, error) {
	bigInt := new(big.Int)

	_, success := bigInt.SetString(hexString[2:], 16) // Remove "0x" prefix
	if !success {
		return nil, errors.New("failed to convert hex string to big.Int")
	}
	return bigInt, nil
}

func Max(old, new *int64) *int64 {
	if new == nil {
		return old
	}
	if old == nil {
		return new
	}
	if *new > *old {
		return new
	}
	return old
}

func SetHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}

func NeuronToA0gi(neuronStr string) (string, error) {
	// 1 neuron = 10^-18 A0GI
	conversionFactor := new(big.Float).SetFloat64(1e-18)

	neuronInt := new(big.Int)
	_, ok := neuronInt.SetString(neuronStr, 10)
	if !ok {
		return "", fmt.Errorf("invalid input string: %s", neuronStr)
	}

	neuronFloat := new(big.Float).SetInt(neuronInt)

	a0giFloat := new(big.Float).Mul(neuronFloat, conversionFactor)

	// Output the result as a string
	return a0giFloat.Text('f', -1), nil // Use 'f' for fixed-point formatting with full precision
}

func ConvertToBigInt(value interface{}) (*big.Int, error) {
	var result big.Int

	switch v := value.(type) {
	case int64:
		result.SetInt64(v)
	case *int64:
		if v == nil {
			return nil, fmt.Errorf("nil pointer to int64")
		}
		result.SetInt64(*v)
	case string:
		if _, ok := result.SetString(v, 10); !ok {
			return nil, fmt.Errorf("invalid string input: %v", v)
		}
	case *string:
		if v == nil {
			return nil, fmt.Errorf("nil pointer to string")
		}
		if _, ok := result.SetString(*v, 10); !ok {
			return nil, fmt.Errorf("invalid string input: %v", *v)
		}
	case big.Int:
		result.Set(&v)
	case *big.Int:
		if v == nil {
			return nil, fmt.Errorf("nil pointer to big.Int")
		}
		result.Set(v)
	default:
		return nil, fmt.Errorf("unsupported type: %T", value)
	}

	return &result, nil
}

func Multiply(a, b interface{}) (*big.Int, error) {
	x, err := ConvertToBigInt(a)
	if err != nil {
		return nil, err
	}

	y, err := ConvertToBigInt(b)
	if err != nil {
		return nil, err
	}

	result := new(big.Int).Mul(x, y)
	return result, nil
}

func Add(a, b interface{}) (*big.Int, error) {
	x, err := ConvertToBigInt(a)
	if err != nil {
		return nil, err
	}

	y, err := ConvertToBigInt(b)
	if err != nil {
		return nil, err
	}

	result := new(big.Int).Add(x, y)
	return result, nil
}

func Compare(a, b interface{}) (int, error) {
	x, err := ConvertToBigInt(a)
	if err != nil {
		return 0, err
	}

	y, err := ConvertToBigInt(b)
	if err != nil {
		return 0, err
	}

	return x.Cmp(y), nil
}
