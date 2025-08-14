package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/spf13/cobra"
)

var (
	jsonOut  bool
	interval time.Duration
	count    int
)

type Snapshot struct {
	Timestamp time.Time `json:"ts"`
	Host      string    `json:"host"`
	OS        string    `json:"os"`
	UptimeSec uint64    `json:"uptime_sec"`

	CPUPercent float64  `json:"cpu_percent"`
	Load1      *float64 `json:"load1,omitempty"`
	Load5      *float64 `json:"load5,omitempty"`
	Load15     *float64 `json:"load15,omitempty"`

	MemUsedMB  uint64  `json:"mem_used_mb"`
	MemTotalMB uint64  `json:"mem_total_mb"`
	MemUsedPct float64 `json:"mem_free_pct"`

	DiskPath    string  `json:"disk_path"`
	DiskUsedGB  float64 `json:"disk_used_gb"`
	DiskTotalGB float64 `json:"disk_total_gb"`
	DiskUsedPct float64 `json:"disk_used_pct"`

	NetBytesIn  uint64 `json:"net_bytes_in"`
	NetBytesOut uint64 `json:"net_bytes_out"`
}

func humanHeader() string {
	return "TIME\tCPU%\tLoad1\tMEM_USED/TOTAL(MB)\tMEM%\tDISK%\tNET_IN/NET_OUT(B)\tHOST"
}

func (s Snapshot) humanRow() string {
	load1 := "-"
	if s.Load1 != nil {
		load1 = fmt.Sprintf("%.2f", *s.Load1)
	}
	return fmt.Sprintf("%s\t%.1f\t%s\t%d/%d\t\t%.1f\t%.1f\t%d/%d\t%s",
		s.Timestamp.Format("15:04:05"),
		s.CPUPercent,
		load1,
		s.MemUsedMB, s.MemTotalMB,
		s.MemUsedPct,
		s.DiskUsedPct,
		s.NetBytesIn, s.NetBytesOut,
		s.Host)
}

func getRootPath() string {
	if runtime.GOOS == "windows" {
		drv := os.Getenv("SystemDrive")
		if drv == "" {
			drv = "C:"
		}
		if !strings.HasSuffix(drv, "\\") {
			drv += "\\"
		}
		return drv
	}
	return "/"
}

func collectOnce(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	now := time.Now()
	snap.Timestamp = now

	hi, _ := host.InfoWithContext(ctx)
	if hi != nil {
		snap.Host = hi.Hostname
		snap.OS = fmt.Sprintf("%s/%s", hi.OS, hi.Platform)
		snap.UptimeSec = hi.Uptime
	}

	// CPU percent (since last call); with interval=10 it uses a short sample window
	pcts, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false)
	if err == nil && len(pcts) > 0 {
		snap.CPUPercent = pcts[0]
	}

	// Load averages
	if runtime.GOOS != "windows" {
		if l, err := load.AvgWithContext(ctx); err == nil && l != nil {
			snap.Load1, snap.Load5, snap.Load15 = &l.Load1, &l.Load5, &l.Load15
		}
	}

	// Memory
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm != nil {
		snap.MemUsedMB = uint64(vm.Used / (1024 * 1024))
		snap.MemTotalMB = uint64(vm.Total / (1024 * 1024))
		snap.MemUsedPct = vm.UsedPercent
	}

	// Disk Usage on root
	root := getRootPath()
	if du, err := disk.UsageWithContext(ctx, root); err == nil && du != nil {
		snap.DiskPath = root
		snap.DiskUsedGB = float64(du.Used) / (1024 * 1024 * 1024)
		snap.DiskTotalGB = float64(du.Total) / (1024 * 1024 * 1024)
		snap.DiskUsedPct = du.UsedPercent
	}

	// Net I/O (all interfaces aggregated)
	if ios, err := net.IOCountersWithContext(ctx, false); err == nil && len(ios) > 0 {
		snap.NetBytesIn = ios[0].BytesRecv
		snap.NetBytesOut = ios[0].BytesSent
	}

	return snap, nil
}

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "Collect basic system stats (single sample or repeated)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if interval <= 0 {
			snap, err := collectOnce(ctx)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(snap)
			}
			fmt.Println(humanHeader())
			fmt.Println(snap.humanRow())
			return nil
		}

		// Streaming mode
		if count < 1 {
			count = 0
		} // 0 = run forever until ctrl-c
		t := time.NewTicker(interval)
		defer t.Stop()

		if !jsonOut {
			fmt.Println(humanHeader())
		}

		i := 0
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				snap, err := collectOnce(ctx)
				if err != nil {
					return err
				}
				if jsonOut {
					b, _ := json.Marshal(snap)
					fmt.Println(string(b))
				} else {
					fmt.Println(snap.humanRow())
				}
				i++
				if count > 0 && i >= count {
					return nil
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(collectCmd)
	collectCmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON instead of table")
	collectCmd.Flags().DurationVar(&interval, "interval", 0, "sampling interval (e.g. 2s); 0 for single sample")
	collectCmd.Flags().IntVar(&count, "count", 1, "number of samples when using --interval (0 = infinite)")
}
