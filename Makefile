.PHONY: build test vet lint scan batch clean tools

BIN := truebug
BATCH := truebug-batch

build:
    go build -o $(BIN) ./cmd/cli
    go build -o $(BATCH) ./cmd/batch

test:
    go test ./...

vet:
    go vet ./...

lint: vet
    staticcheck ./...

tools:
    go install honnef.co/go/tools/cmd/staticcheck@latest

# make scan REPO=../prometheus
scan: build
    ./$(BIN) -repo $(REPO) -out findings.json -negatives negatives.jsonl

batch: build
    ./$(BATCH) -repos bench/repos.txt -jobs 1

clean:
    rm -f $(BIN) $(BATCH) findings.json negatives.jsonl
    rm -rf bench/results