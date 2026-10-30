//go:build !darwin && !linux

package harness

import "errors"

func cloneTree(string, string) error { return errors.ErrUnsupported }

func cloneFile(string, string) error { return errors.ErrUnsupported }
