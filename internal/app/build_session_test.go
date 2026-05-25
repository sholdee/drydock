package app

import "testing"

func TestShouldSnapshotCacheReads(t *testing.T) {
	tests := []struct {
		name    string
		request BuildRequest
		want    bool
	}{
		{
			name: "default request snapshots cache reads",
			want: true,
		},
		{
			name:    "offline read only skips cache read snapshots",
			request: BuildRequest{Offline: true},
		},
		{
			name:    "offline refresh charts snapshots cache reads",
			request: BuildRequest{Offline: true, RefreshCharts: true},
			want:    true,
		},
		{
			name:    "offline refresh git snapshots cache reads",
			request: BuildRequest{Offline: true, RefreshGit: true},
			want:    true,
		},
		{
			name:    "offline refresh remote resources snapshots cache reads",
			request: BuildRequest{Offline: true, RefreshRemoteResources: true},
			want:    true,
		},
		{
			name:    "offline allow network snapshots cache reads",
			request: BuildRequest{Offline: true, AllowNetwork: true},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSnapshotCacheReads(tt.request); got != tt.want {
				t.Fatalf("shouldSnapshotCacheReads() = %t, want %t", got, tt.want)
			}
		})
	}
}
