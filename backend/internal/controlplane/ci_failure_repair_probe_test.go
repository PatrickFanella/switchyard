package controlplane

import (
    "os"
    "testing"
)

func TestCIFailureRepairProbe(t *testing.T) {
    if os.Getenv("CI") != "" {
        t.Fatal("intentional CI repair-loop probe")
    }
}
