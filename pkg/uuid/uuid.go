package uuid

import (
	"strings"

	"github.com/google/uuid"
)

func ShortID() string {
	return strings.Split(uuid.NewString(), "-")[0]
}
