// Package nativescan is the offline native-scanner acceptance slice.
//
// It plans per-item mounts, keeps a local CLI mode that does not require
// Linear, publishes reports into a retention directory, and runs the
// no-network preflight. The public scanner pin is plugin=stub and
// fetch=forbidden. paidProbeAllowed stays false. Nothing here attests an
// actual backend or fetches a private image.
//
// Retention is 168h. Item workspaces are removed on exit, cancel, and
// cleanup. Report and coverage files under retention/ survive that removal.
// There is no shared writable checkout and no shared /art root.
package nativescan
