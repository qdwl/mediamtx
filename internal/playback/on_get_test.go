package playback

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/pkg/formats/fmp4/seekablebuffer"
	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/test"
	"github.com/stretchr/testify/require"
)

func writeSegment1(t *testing.T, fpath string) {
	init := fmp4.Init{
		Tracks: []*fmp4.InitTrack{
			{
				ID:        1,
				TimeScale: 90000,
				Codec: &fmp4.CodecH264{
					SPS: test.FormatH264.SPS,
					PPS: test.FormatH264.PPS,
				},
			},
			{
				ID:        2,
				TimeScale: 90000,
				Codec: &fmp4.CodecMPEG4Audio{
					Config: mpeg4audio.Config{
						Type:         mpeg4audio.ObjectTypeAACLC,
						SampleRate:   48000,
						ChannelCount: 2,
					},
				},
			},
		},
	}

	var buf1 seekablebuffer.Buffer
	err := init.Marshal(&buf1)
	require.NoError(t, err)

	var buf2 seekablebuffer.Buffer
	parts := fmp4.Parts{
		{
			SequenceNumber: 1,
			Tracks: []*fmp4.PartTrack{{
				ID:       1,
				BaseTime: 0,
				Samples:  []*fmp4.PartSample{},
			}},
		},
		{
			SequenceNumber: 2,
			Tracks: []*fmp4.PartTrack{
				{
					ID:       1,
					BaseTime: 30 * 90000,
					Samples: []*fmp4.PartSample{
						{
							Duration:        30 * 90000,
							IsNonSyncSample: false,
							Payload:         []byte{1, 2},
						},
						{
							Duration:        1 * 90000,
							IsNonSyncSample: false,
							Payload:         []byte{3, 4},
						},
						{
							Duration:        1 * 90000,
							IsNonSyncSample: true,
							Payload:         []byte{5, 6},
						},
					},
				},
				{
					ID:       2,
					BaseTime: 29 * 90000,
					Samples: []*fmp4.PartSample{
						{
							Duration:        30 * 90000,
							IsNonSyncSample: false,
							Payload:         []byte{1, 2},
						},
					},
				},
			},
		},
	}
	err = parts.Marshal(&buf2)
	require.NoError(t, err)

	err = os.WriteFile(fpath, append(buf1.Bytes(), buf2.Bytes()...), 0o644)
	require.NoError(t, err)
}

func writeSegment2(t *testing.T, fpath string) {
	init := fmp4.Init{
		Tracks: []*fmp4.InitTrack{
			{
				ID:        1,
				TimeScale: 90000,
				Codec: &fmp4.CodecH264{
					SPS: test.FormatH264.SPS,
					PPS: test.FormatH264.PPS,
				},
			},
			{
				ID:        2,
				TimeScale: 90000,
				Codec: &fmp4.CodecMPEG4Audio{
					Config: mpeg4audio.Config{
						Type:         mpeg4audio.ObjectTypeAACLC,
						SampleRate:   48000,
						ChannelCount: 2,
					},
				},
			},
		},
	}

	var buf1 seekablebuffer.Buffer
	err := init.Marshal(&buf1)
	require.NoError(t, err)

	var buf2 seekablebuffer.Buffer
	parts := fmp4.Parts{
		{
			SequenceNumber: 3,
			Tracks: []*fmp4.PartTrack{{
				ID:       1,
				BaseTime: 0,
				Samples: []*fmp4.PartSample{
					{
						Duration:        1 * 90000,
						IsNonSyncSample: false,
						Payload:         []byte{7, 8},
					},
					{
						Duration:        1 * 90000,
						IsNonSyncSample: false,
						Payload:         []byte{9, 10},
					},
				},
			}},
		},
		{
			SequenceNumber: 4,
			Tracks: []*fmp4.PartTrack{{
				ID:       1,
				BaseTime: 2 * 90000,
				Samples: []*fmp4.PartSample{
					{
						Duration:        1 * 90000,
						IsNonSyncSample: false,
						Payload:         []byte{11, 12},
					},
				},
			}},
		},
	}
	err = parts.Marshal(&buf2)
	require.NoError(t, err)

	err = os.WriteFile(fpath, append(buf1.Bytes(), buf2.Bytes()...), 0o644)
	require.NoError(t, err)
}

func writeSegment3(t *testing.T, fpath string) {
	init := fmp4.Init{
		Tracks: []*fmp4.InitTrack{
			{
				ID:        1,
				TimeScale: 90000,
				Codec: &fmp4.CodecH264{
					SPS: test.FormatH264.SPS,
					PPS: test.FormatH264.PPS,
				},
			},
		},
	}

	var buf1 seekablebuffer.Buffer
	err := init.Marshal(&buf1)
	require.NoError(t, err)

	var buf2 seekablebuffer.Buffer
	parts := fmp4.Parts{
		{
			SequenceNumber: 1,
			Tracks: []*fmp4.PartTrack{{
				ID:       1,
				BaseTime: 0,
				Samples: []*fmp4.PartSample{
					{
						Duration:        1 * 90000,
						IsNonSyncSample: false,
						Payload:         []byte{13, 14},
					},
				},
			}},
		},
	}
	err = parts.Marshal(&buf2)
	require.NoError(t, err)

	err = os.WriteFile(fpath, append(buf1.Bytes(), buf2.Bytes()...), 0o644)
	require.NoError(t, err)
}

var authManager = &auth.Manager{
	Method: conf.AuthMethodInternal,
	InternalUsers: []conf.AuthInternalUser{
		{
			User: "myuser",
			Pass: "mypass",
			Permissions: []conf.AuthInternalUserPermission{
				{
					Action: conf.AuthActionPlayback,
					Path:   "mypath",
				},
			},
		},
	},
	RTSPAuthMethods: nil,
}

func TestOnGet(t *testing.T) {
	dir, err := os.MkdirTemp("", "mediamtx-playback")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	err = os.Mkdir(filepath.Join(dir, "mypath"), 0o755)
	require.NoError(t, err)

	writeSegment1(t, filepath.Join(dir, "mypath", "2008-11-07_11-22-00-500000.mp4"))
	writeSegment2(t, filepath.Join(dir, "mypath", "2008-11-07_11-23-02-500000.mp4"))
	writeSegment2(t, filepath.Join(dir, "mypath", "2008-11-07_11-23-04-500000.mp4"))

	s := &Server{
		Address:     "127.0.0.1:9996",
		ReadTimeout: conf.StringDuration(10 * time.Second),
		PathConfs: map[string]*conf.Path{
			"mypath": {
				RecordPath: filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f"),
			},
		},
		AuthManager: authManager,
		Parent:      &test.NilLogger{},
	}
	err = s.Initialize()
	require.NoError(t, err)
	defer s.Close()

	u, err := url.Parse("http://myuser:mypass@localhost:9996/get")
	require.NoError(t, err)

	v := url.Values{}
	v.Set("path", "mypath")
	v.Set("start", time.Date(2008, 11, 0o7, 11, 23, 1, 500000000, time.Local).Format(time.RFC3339Nano))
	v.Set("duration", "3")
	v.Set("format", "fmp4")
	u.RawQuery = v.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)

	buf, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	var parts fmp4.Parts
	err = parts.Unmarshal(buf)
	require.NoError(t, err)

	require.Equal(t, fmp4.Parts{
		{
			SequenceNumber: 0,
			Tracks: []*fmp4.PartTrack{
				{
					ID: 1,
					Samples: []*fmp4.PartSample{
						{
							Duration: 0,
							Payload:  []byte{3, 4},
						},
						{
							Duration:        90000,
							IsNonSyncSample: true,
							Payload:         []byte{5, 6},
						},
						{
							Duration: 90000,
							Payload:  []byte{7, 8},
						},
					},
				},
			},
		},
		{
			SequenceNumber: 1,
			Tracks: []*fmp4.PartTrack{
				{
					ID:       1,
					BaseTime: 180000,
					Samples: []*fmp4.PartSample{
						{
							Duration: 90000,
							Payload:  []byte{9, 10},
						},
					},
				},
			},
		},
	}, parts)
}

func TestOnGetDifferentInit(t *testing.T) {
	dir, err := os.MkdirTemp("", "mediamtx-playback")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	err = os.Mkdir(filepath.Join(dir, "mypath"), 0o755)
	require.NoError(t, err)

	writeSegment1(t, filepath.Join(dir, "mypath", "2008-11-07_11-22-00-500000.mp4"))
	writeSegment3(t, filepath.Join(dir, "mypath", "2008-11-07_11-23-02-500000.mp4"))

	s := &Server{
		Address:     "127.0.0.1:9996",
		ReadTimeout: conf.StringDuration(10 * time.Second),
		PathConfs: map[string]*conf.Path{
			"mypath": {
				RecordPath: filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f"),
			},
		},
		AuthManager: authManager,
		Parent:      &test.NilLogger{},
	}
	err = s.Initialize()
	require.NoError(t, err)
	defer s.Close()

	u, err := url.Parse("http://myuser:mypass@localhost:9996/get")
	require.NoError(t, err)

	v := url.Values{}
	v.Set("path", "mypath")
	v.Set("start", time.Date(2008, 11, 0o7, 11, 23, 1, 500000000, time.Local).Format(time.RFC3339Nano))
	v.Set("duration", "2")
	v.Set("format", "fmp4")
	u.RawQuery = v.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)

	buf, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	var parts fmp4.Parts
	err = parts.Unmarshal(buf)
	require.NoError(t, err)

	require.Equal(t, fmp4.Parts{
		{
			SequenceNumber: 0,
			Tracks: []*fmp4.PartTrack{
				{
					ID: 1,
					Samples: []*fmp4.PartSample{
						{
							Duration: 0,
							Payload:  []byte{3, 4},
						},
						{
							Duration:        90000,
							IsNonSyncSample: true,
							Payload:         []byte{5, 6},
						},
					},
				},
			},
		},
	}, parts)
}

func TestOnGetNTPCompensation(t *testing.T) {
	dir, err := os.MkdirTemp("", "mediamtx-playback")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	err = os.Mkdir(filepath.Join(dir, "mypath"), 0o755)
	require.NoError(t, err)

	writeSegment1(t, filepath.Join(dir, "mypath", "2008-11-07_11-22-00-500000.mp4"))
	writeSegment2(t, filepath.Join(dir, "mypath", "2008-11-07_11-23-02-000000.mp4")) // remove 0.5 secs

	s := &Server{
		Address:     "127.0.0.1:9996",
		ReadTimeout: conf.StringDuration(10 * time.Second),
		PathConfs: map[string]*conf.Path{
			"mypath": {
				RecordPath: filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f"),
			},
		},
		AuthManager: authManager,
		Parent:      &test.NilLogger{},
	}
	err = s.Initialize()
	require.NoError(t, err)
	defer s.Close()

	u, err := url.Parse("http://myuser:mypass@localhost:9996/get")
	require.NoError(t, err)

	v := url.Values{}
	v.Set("path", "mypath")
	v.Set("start", time.Date(2008, 11, 0o7, 11, 23, 1, 500000000, time.Local).Format(time.RFC3339Nano))
	v.Set("duration", "3")
	v.Set("format", "fmp4")
	u.RawQuery = v.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)

	buf, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	var parts fmp4.Parts
	err = parts.Unmarshal(buf)
	require.NoError(t, err)

	require.Equal(t, fmp4.Parts{
		{
			SequenceNumber: 0,
			Tracks: []*fmp4.PartTrack{
				{
					ID: 1,
					Samples: []*fmp4.PartSample{
						{
							Duration: 0,
							Payload:  []byte{3, 4},
						},
						{
							Duration:        45000, // 90 - 45
							IsNonSyncSample: true,
							Payload:         []byte{5, 6},
						},
						{
							Duration: 90000,
							Payload:  []byte{7, 8},
						},
					},
				},
			},
		},
		{
			SequenceNumber: 1,
			Tracks: []*fmp4.PartTrack{
				{
					ID:       1,
					BaseTime: 135000,
					Samples: []*fmp4.PartSample{
						{
							Duration: 90000,
							Payload:  []byte{9, 10},
						},
						{
							Duration: 90000,
							Payload:  []byte{11, 12},
						},
					},
				},
			},
		},
	}, parts)
}
