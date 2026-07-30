package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ubusCall je jedino mjesto u Core-u koje dohvaća stanje sustava.
// ubus vraća čisti JSON pa nema parsiranja tekstualnih izlaza (D-007).
func ubusCall(ctx context.Context, object, method string, out any) error {
	return ubusCallArg(ctx, object, method, "", out)
}

// ubusCallArg je varijanta s JSON argumentom (npr. uci get {"config":"dhcp"}).
func ubusCallArg(ctx context.Context, object, method, arg string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"call", object, method}
	if arg != "" {
		args = append(args, arg)
	}
	b, err := exec.CommandContext(ctx, "ubus", args...).Output()
	if err != nil {
		return fmt.Errorf("ubus call %s %s: %w", object, method, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("ubus %s %s decode: %w", object, method, err)
	}
	return nil
}

// uciBatch izvrši niz uci naredbi (uključivo commit) kao jednu atomarnu seriju.
func uciBatch(ctx context.Context, script string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "uci", "batch")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("uci batch: %v: %s", err, out)
	}
	return nil
}

// serviceReload pozove init skriptu servisa (npr. dnsmasq reload).
func serviceReload(ctx context.Context, name, action string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/etc/init.d/"+name, action).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, action, err, out)
	}
	return nil
}
