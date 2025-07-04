package core

import (
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"

	"github.com/Layr-Labs/eigensdk-go/logging"
	"golang.org/x/exp/constraints"
)

func RoundUpDivideBig(a, b *big.Int) *big.Int {

	one := new(big.Int).SetUint64(1)
	num := new(big.Int).Sub(new(big.Int).Add(a, b), one) // a + b - 1
	res := new(big.Int).Div(num, b)                      // (a + b - 1) / b
	return res
}

func RoundUpDivide[T constraints.Integer](a, b T) T {
	return (a + b - 1) / b
}

func NextPowerOf2[T constraints.Integer](d T) T {
	nextPower := math.Ceil(math.Log2(float64(d)))
	return T(math.Pow(2.0, nextPower))
}

func ValidatePort(portStr string) error {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port is not a valid number: %v", err)
	}

	if port < 1 || port > 65535 {
		return fmt.Errorf("port number out of valid range (1-65535)")
	}
	return err
}

// CloseLogOnError attempts to close the given io.Closer and logs an error if it fails.
// Meant to be called in a defer statement: defer CloseLogOnError(c, "nameOfResourceToClose", log).
func CloseLogOnError(c io.Closer, name string, log logging.Logger) {
	if closeErr := c.Close(); closeErr != nil {
		if log != nil {
			log.Errorf("failed to close %s: %s", name, closeErr.Error())
		} else {
			fmt.Printf("failed to close %s: %s", name, closeErr.Error())
		}
	}
}
