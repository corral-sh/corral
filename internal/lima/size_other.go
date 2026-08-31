//go:build !darwin

package lima

import "os"

func allocatedSize(info os.FileInfo) int64 { return info.Size() }
