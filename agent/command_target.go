package main

import (
	"context"
	"errors"
	"kvm-center/internal/models"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var commandUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func resolveVMTarget(req models.CommandRequest, lookup func(string, string) (string, error)) (models.CommandRequest, error) {
	if strings.ToUpper(req.Action) == "RETIRE" {
		return req, nil
	}
	target := req.VMUUID
	if target == "" {
		target = req.VMName
	}
	if target == "" {
		return req, errors.New("VM command requires a target")
	}
	uuid, err := lookup("domuuid", target)
	if err != nil {
		return req, err
	}
	uuid = strings.TrimSpace(uuid)
	if !commandUUIDPattern.MatchString(uuid) {
		return req, errors.New("native UUID unavailable")
	}
	if req.VMUUID != "" && !strings.EqualFold(uuid, req.VMUUID) {
		return req, errors.New("native UUID mismatch")
	}
	name, err := lookup("domname", uuid)
	if err != nil {
		return req, err
	}
	name = strings.TrimSpace(name)
	if name == "" || (req.VMName != "" && name != req.VMName) {
		return req, errors.New("native UUID/name mismatch")
	}
	req.VMUUID = strings.ToLower(uuid)
	req.VMName = name
	return req, nil
}
func resolveNativeCommandTarget(req models.CommandRequest) (models.CommandRequest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return resolveVMTarget(req, func(verb, target string) (string, error) {
		cmd := exec.CommandContext(ctx, "virsh", verb, "--", target)
		cmd.WaitDelay = time.Second
		raw, err := cmd.Output()
		return string(raw), err
	})
}

// Only synchronous power dispatch has a UUID-based native execution contract.
// Migration and other existing handlers also use their target for display names
// and destination naming; retain the resolved canonical name for those actions.
func commandExecutionTarget(req models.CommandRequest) string {
	switch strings.ToUpper(req.Action) {
	case "START", "STOP", "FORCESTOP", "RESTART":
		if req.VMUUID != "" {
			return req.VMUUID
		}
	}
	if req.VMName != "" {
		return req.VMName
	}
	return req.VMUUID
}
