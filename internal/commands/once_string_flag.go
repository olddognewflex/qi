package commands

import "errors"

type onceStringValue struct {
	value string
	set   bool
}

func (v *onceStringValue) Set(value string) error {
	if value == "" {
		return errors.New("value must not be empty")
	}
	if v.set {
		return errors.New("value may be set only once")
	}

	v.value = value
	v.set = true
	return nil
}

func (v *onceStringValue) String() string {
	return v.value
}

func (v *onceStringValue) Type() string {
	return "string"
}
