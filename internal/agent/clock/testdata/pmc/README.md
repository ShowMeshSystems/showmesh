# pmc fixtures

Every `*.master.txt` and `*.listening.txt` file here is unedited `pmc`
output, captured against a real `ptp4l` 4.2-1 (Debian trixie) process
running in software timestamping mode on this VM (see
`docs/build/BUILD-LOG.md` for the seam this shipped with). Command line:
`pmc -u -b 0 -d <domain> -s <uds-ro-address> 'GET <name>'`.

This sandboxed VM's loopback interface does not deliver PTP multicast
traffic between two independent `ptp4l` processes (each one becomes its
own grandmaster instead of one syncing to the other — see the seam's PR
for the observed log), so a genuine SLAVE-role capture, and a genuine
PTP-timescale (`ptpTimescale 1`) capture, were not obtainable here (this
build's `ptp4l`, free-running with no upstream, reports `ptpTimescale 0`
i.e. ARB). `*.slave.txt` and `time_properties_data_set.ptp.txt` are the
real captures above with only specific field VALUES edited by hand
(`portState`/`gmPresent`/`master_offset`/`gmIdentity` for the slave
fixtures, `ptpTimescale`/`currentUtcOffset` for the PTP-timescale one) to
a plausible reading; every field name, column alignment, and surrounding
format byte is untouched real `pmc` output.
