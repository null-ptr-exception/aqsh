# Source provenance

Ported from https://github.com/rophy/kube-auth-proxy/tree/bdc304cba135e919b9c95f4d7515958dec9624c5/internal/proxy (0.4.1).
The original source files, tests and MIT license are retained.

Local integration changes expose construction with an injected HTTP client and
reviewer token source, carry UID/audiences through the existing wire types,
and reject redirects/non-success responses at the aqsh boundary. The aqsh API
uses this TokenReview client directly; no standalone proxy listener is started.
