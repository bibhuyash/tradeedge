package model

import "math"

func CheckedAdd(left, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
		return 0, ErrArithmeticOverflow
	}
	return left + right, nil
}

func CheckedSub(left, right int64) (int64, error) {
	if right == math.MinInt64 {
		return 0, ErrArithmeticOverflow
	}
	return CheckedAdd(left, -right)
}

func CheckedMul(left, right int64) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	if (left == -1 && right == math.MinInt64) || (right == -1 && left == math.MinInt64) {
		return 0, ErrArithmeticOverflow
	}
	value := left * right
	if value/right != left {
		return 0, ErrArithmeticOverflow
	}
	return value, nil
}

// MulDiv calculates value*numerator/denominator with checked multiplication.
// When ceil is true, a positive remainder rounds upward; otherwise it truncates.
func MulDiv(value, numerator, denominator int64, ceil bool) (int64, error) {
	if value < 0 || numerator < 0 || denominator <= 0 {
		return 0, ErrArithmeticOverflow
	}
	product, err := CheckedMul(value, numerator)
	if err != nil {
		return 0, err
	}
	result := product / denominator
	if ceil && product%denominator != 0 {
		return CheckedAdd(result, 1)
	}
	return result, nil
}
