# FPP MQTT captures

Real retained and live payloads from FPP's own MQTT topics under
`falcon/player/<host>/`, captured read-only on 2026-08-11 during the Step 5 live
probe. The filename prefix is the publishing host.

These are a second source for signals the REST collector also reports, which is
the point: the two disagree in ways that matter. `fpp-remote-b` omits `warnings`
entirely over REST while its MQTT `warnings` topic publishes `[]`, so absence and
emptiness describe the same fact by different means and a collector that
conflates them is wrong. `fpp-ghost` exists only as retained state, never
observed live, and is the acceptance case for the retained-message rule.

## What was substituted, and what was not

**Deployment identity was replaced on 2026-08-14. Payload shape was not
touched.** The substitution is the same mapping used in
`../../fpp/testdata/README.md`, applied identically here and to the filenames,
so a host's REST capture and its MQTT capture still agree on its name.

Note that identity appears in more than values in this directory:
`fpp-ghost_ha_sensor_VIN1_config.json` carries the host name inside
`unique_id`, `identifiers`, a `state_topic` and a `configuration_url`. Those
were substituted too, and the topic and URL structure around them is unchanged.

Everything else is verbatim, including the cJSON-style indentation FPP itself
emits, key presence and absence, and the number-encoded booleans that
`json.Unmarshal` would silently accept into the wrong type.

## The rule these files exist to enforce

**A failing test here means the decoder is wrong, not the fixture.** If FPP's
real behaviour has genuinely changed, that needs a new capture and a note saying
so, not an edit.
