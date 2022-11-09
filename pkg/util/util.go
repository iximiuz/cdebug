package util

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func ShortID() string {
	return strings.Split(uuid.NewString(), "-")[0]
}

func PrettyPrint(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}
