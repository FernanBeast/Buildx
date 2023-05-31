package buildflags

import (
	"strings"

	controllerapi "github.com/docker/buildx/controller/pb"
	"github.com/docker/distribution/reference"
	"github.com/pkg/errors"
)

func ParseContextNames(values []string) (map[string]*controllerapi.NamedContext, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]*controllerapi.NamedContext, len(values))
	for _, value := range values {
		kv := strings.SplitN(value, "=", 2)
		if len(kv) != 2 {
			return nil, errors.Errorf("invalid context value: %s, expected key=value", value)
		}
		named, err := reference.ParseNormalizedNamed(kv[0])
		if err != nil {
			return nil, errors.Wrapf(err, "invalid context name %s", kv[0])
		}
		name := strings.TrimSuffix(reference.FamiliarString(named), ":latest")
		result[name] = &controllerapi.NamedContext{Path: kv[1]}
	}
	return result, nil
}
