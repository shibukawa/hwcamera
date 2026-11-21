package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/shibukawa/hwcamera/audio"
)

func mics(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mics", flag.ExitOnError)
	monitors := fs.Bool("monitors", false, "also list the monitors of the outputs (Linux)")
	fs.Parse(args)
	ds, err := audio.Devices(ctx)
	if err != nil {
		return err
	}
	shown := 0
	for _, d := range ds {
		if d.Monitor && !*monitors {
			continue
		}
		mark := " "
		if d.Default {
			mark = "*"
		}
		kind := ""
		if d.Monitor {
			kind = ", monitor"
		}
		fmt.Printf("%s %s  \"%s\"  [%s%s, %d Hz, %d ch]\n", mark, d.ID, d.Name, d.Backend, kind, d.SampleRate, d.Channels)
		shown++
	}
	if shown == 0 {
		fmt.Println("no audio inputs (backends:", strings.Join(audio.Backends(), ", ")+")")
	}
	return nil
}
