# The end-to-end lane in a box: the Go toolchain, a Chromium, and nothing of the
# host. It exists so the hub, control, and vault can be tested against a real
# browser and real child processes without touching a developer's machine, their
# accounts, or their browsers.
#
# Build and run from the repository root:
#
#	docker build -f e2e/Dockerfile -t midas-e2e .
#	docker run --rm midas-e2e
#
# The suite builds the four servers from this checkout, so the image runs exactly
# the code in the build context.
FROM golang:1.27-bookworm

# A real browser for control to attach to. Debian's chromium is headless capable,
# which is what the container has: no display, no desktop session.
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# The module graph first, so a rebuild does not download it again.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The suite finds this by name; being explicit keeps discovery from picking a
# different browser if the image ever has more than one.
ENV MIDAS_E2E_BROWSER=/usr/bin/chromium
# Nothing here needs cgo, and the voice package cannot build without its prebuilt
# libraries, so the lane stays pure Go.
ENV CGO_ENABLED=0
ENV GOFLAGS=-count=1

CMD ["go", "test", "./e2e/", "-count=1", "-timeout", "15m"]
