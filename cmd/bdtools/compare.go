package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/brunoga/bdtools/internal/convert"
	"github.com/brunoga/bdtools/internal/vmaf"
)

// runCompare measures an output against its source (--compare).
func runCompare(src, out string, skip, every int, csvPath, temp string, quiet bool, stdout, stderr *os.File) int {
	if src == "" {
		fmt.Fprintln(stderr, "bdtools: --compare needs --input, the source")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := convert.Reporter(func(format string, args ...any) {
		if !quiet {
			fmt.Fprintf(stderr, "bdtools: "+format+"\n", args...)
		}
	})
	o := convert.DefaultOptions()
	o.TempDir = temp
	res, err := convert.Compare(ctx, runtime.GOOS, o, src, out, skip, every, report)
	if err != nil {
		fmt.Fprintln(stderr, "bdtools:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%d pictures (%s)\n", res.Pictures, res.Model)
	fmt.Fprintf(stdout, "%-6s %9s %9s %9s %9s\n", "", "mean", "harmonic", "1%", "min")
	line := func(name string, p vmaf.Pooled) {
		fmt.Fprintf(stdout, "%-6s %9.4f %9.4f %9.4f %9.4f\n", name, p.Mean, p.Harmonic, p.P1, p.Min)
	}
	line("VMAF", res.VMAF)
	line("PSNR", res.PSNR)
	line("SSIM", res.SSIM)
	if csvPath != "" {
		f, err := os.Create(csvPath) //nolint:gosec // the operator's path
		if err != nil {
			fmt.Fprintln(stderr, "bdtools:", err)
			return 1
		}
		w := csv.NewWriter(f)
		_ = w.Write([]string{"picture", "vmaf", "psnr_y", "ssim", "adm2", "motion2", "vif_scale0", "vif_scale1", "vif_scale2", "vif_scale3"})
		ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
		for i, s := range res.Scores {
			_ = w.Write([]string{strconv.Itoa(skip + i*every), ff(s.VMAF), ff(s.PSNR), ff(s.SSIM), ff(s.ADM2), ff(s.Motion2),
				ff(s.VIF[0]), ff(s.VIF[1]), ff(s.VIF[2]), ff(s.VIF[3])})
		}
		w.Flush()
		if err := errors.Join(w.Error(), f.Close()); err != nil {
			fmt.Fprintln(stderr, "bdtools:", err)
			return 1
		}
	}
	return 0
}
