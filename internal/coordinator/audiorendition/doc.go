// Package audiorendition decodes an uploaded audio asset (WAV, MP3, FLAC,
// or Ogg Vorbis, identified by its actual content, never its filename) and
// renders it as a 48 kHz, 16-bit, stereo RIFF PCM WAV file: the fixed
// format every node opens in milliseconds and knows the duration of,
// unlike the slow, variable MP3 decode a node otherwise runs at show-load
// time.
//
// Every decoder here is pure Go (github.com/hajimehoshi/go-mp3,
// github.com/mewkiz/flac, github.com/jfreymuth/oggvorbis), so neither the
// distroless coordinator image nor an arm64 build needs a system decoder.
// A mono source is duplicated to both output channels; any other sample
// rate is resampled with a windowed-sinc filter, never nearest-sample.
//
// [DetectFormat] is the fast, header-only synchronous check the upload
// path runs to refuse an unsupported file immediately. [Render] runs the
// full decode, resample, and re-encode, and is deliberately never called
// from a request handler; see [Service] for where it runs instead.
package audiorendition
