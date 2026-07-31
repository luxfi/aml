# The AML console: a compiled bundle, served by the house static server.
#
# It is its own image because it is its own deploy unit. The API is reached at
# api.<brand>/v1/aml and this bundle is served on its own host — one door for
# data, one for the console, and neither inside the other. Building it into the
# daemon would put them back in one pod and one blast radius.
#
# npm with the committed lockfile: `npm ci` fails on lockfile drift rather than
# resolving fresh, and needs nothing in the image but the node base.
FROM node:22-alpine AS build
WORKDIR /ui
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# ghcr.io/hanzoai/static serves it. -spa sends unknown paths to index.html, which
# is what makes /cases/… and /callback resolve on a hard refresh; the security
# headers and the CSP come from the server.
#
# Nothing is templated into the bundle at startup and no SPA_* env is set, so the
# served root is never written and the container runs read-only and unprivileged.
# The console derives its API from the host it is served on and reads its brand,
# issuer and clientId from that API — so this image is every brand's, unchanged.
FROM ghcr.io/hanzoai/static:v0.5.1
COPY --from=build /ui/dist /public
EXPOSE 3000
ENTRYPOINT ["/static", "-port", "3000", "-root", "/public", "-spa"]
