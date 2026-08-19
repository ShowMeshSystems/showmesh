/* Bench-only external LTC generator, R3 candidate (c): "an external
 * generator (ltcgen or similar) feeding the pipeline through fdsrc/filesrc
 * -- still not a Go sample path". Debian 13 ships libltc11/libltc-dev (the
 * real Manchester-biphase LTC encoder) but no prebuilt ltcgen binary, so
 * this is that binary, built against the real library rather than any
 * hand-rolled bit encoding. It writes raw 8-bit unsigned PCM to stdout at
 * the given sample rate/fps for the given duration; the run scripts wrap
 * it in `gst-launch-1.0 fdsrc ! audio/x-raw,format=U8,... `.
 *
 * Not part of the ShowMesh product; see README.md.
 */
#include <ltc.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc != 4 && argc != 7) {
        fprintf(stderr,
                "usage: %s <sample_rate> <fps> <duration_seconds> "
                "[start_hh start_mm start_ss]\n", argv[0]);
        return 2;
    }
    double sample_rate = atof(argv[1]);
    double fps = atof(argv[2]);
    double duration = atof(argv[3]);
    int start_hh = argc == 7 ? atoi(argv[4]) : 0;
    int start_mm = argc == 7 ? atoi(argv[5]) : 0;
    int start_ss = argc == 7 ? atoi(argv[6]) : 0;

    LTCEncoder *e = ltc_encoder_create(sample_rate, fps, LTC_TV_625_50, 0);
    if (!e) {
        fprintf(stderr, "ltc_encoder_create failed\n");
        return 1;
    }

    SMPTETimecode st;
    memset(&st, 0, sizeof(st));
    st.hours = (unsigned char)start_hh;
    st.mins = (unsigned char)start_mm;
    st.secs = (unsigned char)start_ss;
    st.frame = 0;
    ltc_encoder_set_timecode(e, &st);

    long total_frames = (long)(duration * fps);
    for (long f = 0; f < total_frames; f++) {
        ltc_encoder_encode_frame(e);
        ltcsnd_sample_t *buf;
        int size;
        int ret = ltc_encoder_get_bufferptr(e, &buf, 1);
        (void)ret;
        size = (int)ltc_encoder_get_buffersize(e);
        fwrite(buf, 1, size, stdout);
        ltc_encoder_buffer_flush(e);
        ltc_encoder_inc_timecode(e);
    }

    SMPTETimecode end;
    ltc_encoder_get_timecode(e, &end);
    fprintf(stderr, "ltcgen: start=%02d:%02d:%02d:%02d end=%02d:%02d:%02d:%02d frames=%ld\n",
            st.hours, st.mins, st.secs, st.frame,
            end.hours, end.mins, end.secs, end.frame, total_frames);

    ltc_encoder_free(e);
    return 0;
}
