package tags

// The vocabulary, one topic per line:
//
//	id|Label|priority|terms|regexes
//
// terms is a comma separated list of words and phrases matched on word
// boundaries after the text is split into words (camelCase and snake_case
// included) and lightly stemmed. A term starting with ! is strong (one match is
// worth two ordinary ones) and one starting with p: is a path fragment matched
// against the document's relative path. regexes is an optional list separated
// by ;; of anchored patterns (RE2) for shapes a word table cannot say, such as
// CVE-2024-1234; each match counts as a strong term. priority is 1 for a topic
// that is common and worth asking a backend about first, 3 for a niche one.
//
// Labels are what a decision backend reads, so they are short noun phrases.
// Ids are lower case letters, digits and hyphens, unique, and stable: they are
// stored in the index and typed in searches.

const vocabSecurity = `
authn|Authentication and login|1|!authentication, !login, signin, sign in, password, credential, session cookie, !mfa, 2fa, totp, !passkey, webauthn, !sso, saml, p:/auth|
authz|Authorization and access control|1|!authorization, !rbac, !abac, access control, permission, acl, privilege, role, policy, allowlist, denylist, least privilege|
oauth|OAuth and OpenID Connect|2|!oauth, !oidc, openid, access token, refresh token, bearer, authorization code, pkce, client secret, idp|
jwt|JSON Web Tokens|2|!jwt, json web token, jws, jwe, jwk, jwks, claims, bearer token|
session-mgmt|Session management|2|session, cookie, samesite, httponly, session fixation, logout, idle timeout|
xss-csrf|XSS and CSRF|1|!xss, !csrf, cross site, content security policy, csp, sanitize, escape html, dompurify, innerhtml|
sql-injection|SQL injection|1|!sql injection, parameterized query, prepared statement, sqli, query concatenation|
injection|Injection flaws|1|!injection, command injection, shell injection, template injection, ldap injection, xxe, deserialization, path traversal, !rce, remote code execution|
ssrf|Server-side request forgery|2|!ssrf, server side request forgery, metadata endpoint, internal address, loopback, host allowlist|
crypto|Cryptography|1|!cryptography, encryption, decryption, cipher, aes, rsa, ecdsa, ed25519, !hmac, sha256, hash, nonce, key derivation, argon2, bcrypt, scrypt, pbkdf2|
tls|TLS and certificates|2|!tls, ssl, !certificate, x509, mtls, handshake, ca bundle, pinning, acme, cipher suite|
secrets|Secrets management|1|!secret, !api key, credential, vault, kms, secrets manager, dotenv, rotation, leaked, hardcoded|\b(?:AKIA|ASIA)[0-9A-Z]{16}\b;;-----BEGIN [A-Z ]*PRIVATE KEY-----
secret-scanning|Secret scanning|2|secret scanning, trufflehog, gitleaks, detect secrets, leaked credential, entropy|
pki|Public key infrastructure|3|pki, root ca, intermediate, csr, crl, ocsp, keystore, truststore|
threat-modeling|Threat modeling|2|!threat model, stride, attack surface, attack tree, trust boundary, adversary, abuse case|
pentest|Penetration testing|2|!pentest, penetration test, red team, exploit, payload, poc, metasploit, burp, ctf|
vuln-mgmt|Vulnerability management|1|!vulnerability, !cve, cvss, epss, kev, advisory, remediation, patch, severity, triage, exposure|\bCVE-\d{4}-\d{4,}\b
weakness-cwe|Weakness classes (CWE)|2|!cwe, weakness, owasp, top 10, asvs, mitre, capec|\bCWE-\d{1,4}\b
sast|Static analysis security|2|!sast, static analysis, semgrep, codeql, sonarqube, taint analysis, data flow, finding|
dast|Dynamic security testing|3|!dast, zap, nuclei, crawler, iast, runtime scan|
sca|Software composition analysis|1|!sca, software composition analysis, dependency scan, transitive, vulnerable dependency, osv, ghsa, nvd, advisory database|\bGHSA(?:-[a-z0-9]{4}){3}\b
sbom|Software bill of materials|1|!sbom, !cyclonedx, !spdx, bill of materials, component, purl, package url, syft, cpe|
vex|VEX statements|2|!vex, !openvex, not affected, under investigation, justification, csaf|
sarif|SARIF results|2|!sarif, ruleid, physical location, runs|
supply-chain|Supply chain security|1|!supply chain, provenance, slsa, sigstore, cosign, attestation, in toto, typosquat, dependency confusion, reproducible build, lockfile, pinning|
code-signing|Code signing|3|code signing, gpg, pgp, minisign, notarization, signature, verify signature, tuf|
container-security|Container security|2|container security, image scan, trivy, grype, distroless, rootless, seccomp, apparmor, privileged|
cloud-security|Cloud security posture|2|cspm, security group, bucket policy, public bucket, misconfiguration, cis benchmark, guardduty|
iam|Identity and access management|2|!iam, principal, role assumption, service account, workload identity, sts, trust policy, federation|
network-security|Network security|2|firewall, waf, ids, ips, ddos, rate limit, segmentation, vpn, bastion|
zero-trust|Zero trust|3|zero trust, ztna, beyondcorp, device posture, continuous verification|
malware|Malware and threats|3|malware, ransomware, trojan, backdoor, ioc, yara, phishing, apt|
incident-response|Incident response|2|!incident response, dfir, forensics, containment, eradication, evidence, timeline, playbook|
detection-siem|Detection and SIEM|3|siem, detection rule, sigma, splunk, alert, correlation, soc|
data-protection|Data protection|2|pii, personal data, anonymization, pseudonymization, redaction, data masking, tokenization, dlp|
fuzzing|Fuzzing|3|!fuzz, fuzzing, afl, libfuzzer, corpus, crash, sanitizer, asan|
memory-safety|Memory safety|2|memory safety, buffer overflow, use after free, heap, bounds check, unsafe, double free|
sandboxing|Sandboxing and isolation|2|sandbox, bubblewrap, namespace, chroot, jail, isolation, landlock, gvisor|
prompt-injection|Prompt injection|1|!prompt injection, jailbreak, indirect injection, untrusted content, guardrail, classifier, tool poisoning|
ai-supply-chain|AI supply chain|3|model provenance, aibom, model card, pickle, safetensors, poisoned, model weights|
security-headers|HTTP security headers|3|hsts, x frame options, content type options, referrer policy, permissions policy, cors|
input-validation|Input validation|2|validation, validate, sanitize, whitelist, schema validation, untrusted input, escape|
rate-limiting|Rate limiting and abuse|3|rate limit, throttle, quota, abuse, bot, captcha, brute force|
audit-logging|Audit logging|2|audit log, audit trail, tamper evident, hash chain, accountability, non repudiation|
`

const vocabLanguages = `
lang-go|Go language|2|!golang, goroutine, go mod, gofmt, go vet, go test, defer, waitgroup, p:go.mod|
lang-python|Python language|2|!python, pip, virtualenv, venv, pytest, django, flask, fastapi, pydantic, asyncio, pyproject|
lang-javascript|JavaScript|2|!javascript, node, npm, yarn, pnpm, commonjs, esm, promise, async await, webpack, babel|
lang-typescript|TypeScript|2|!typescript, tsconfig, tsc, interface, generics, type guard, declaration file|
lang-rust|Rust language|2|!rust, cargo, crate, borrow checker, lifetime, trait, tokio, serde, rustc|
lang-java|Java and JVM|2|!java, jvm, maven, gradle, spring, jar, servlet, junit, hibernate, bytecode|
lang-kotlin|Kotlin|3|!kotlin, coroutine, ktor, kotlinx, gradle kts|
lang-ruby|Ruby and Rails|3|!ruby, rails, gem, bundler, rake, rspec, activerecord, sinatra|
lang-php|PHP|3|!php, composer, laravel, symfony, wordpress, phpunit|
lang-csharp|C# and .NET|2|!csharp, dotnet, nuget, asp net, linq, msbuild, csproj, entity framework|
lang-swift|Swift|3|!swift, swiftui, xcode, cocoapods, objective c, uikit|
lang-cpp|C and C++|2|!cpp, c plus plus, cmake, header, pointer, template, stl, gcc, clang, makefile|
lang-shell|Shell scripting|2|!bash, shell script, posix sh, shebang, zsh, fish, awk, sed, pipe, exit code|
lang-sql|SQL language|2|!sql, select, join, group by, index, query plan, stored procedure, view, transaction|
lang-scala|Scala|3|!scala, sbt, akka, spark, case class, implicit|
lang-elixir|Elixir and Erlang|3|!elixir, erlang, beam, otp, phoenix, genserver, mix|
lang-haskell|Haskell and functional|3|!haskell, monad, functor, cabal, ghc, ocaml, lambda calculus|
lang-lua|Lua|3|!lua, luajit, neovim plugin, love2d|
lang-dart|Dart and Flutter|3|!dart, flutter, widget, pubspec|
lang-r|R and statistics|3|!rlang, tidyverse, ggplot, dataframe, cran, rstudio|
lang-perl|Perl|3|!perl, cpan, regex heavy, one liner|
lang-wasm|WebAssembly|3|!wasm, webassembly, wasi, wat, emscripten, wasmtime|
lang-zig|Zig and systems languages|3|!zig, comptime, nim, crystal, d language|
lang-solidity|Solidity|3|!solidity, evm, hardhat, foundry, smart contract|
`

const vocabWebAPI = `
rest-api|REST APIs|1|!rest, !api, endpoint, http method, status code, resource, json response, openapi, swagger, pagination, idempotent|
graphql|GraphQL|2|!graphql, resolver, schema, mutation, subscription, apollo|
grpc|gRPC and protobuf|2|!grpc, !protobuf, proto, rpc, stub, streaming, protoc|
websocket|WebSockets and streaming|3|websocket, server sent events, sse, long polling, realtime, socket|
http|HTTP protocol|2|!http, header, request, response, cookie, keep alive, http2, http3, quic, user agent, redirect|
openapi|OpenAPI specification|2|!openapi, swagger, spec, paths, components, operation id|
webhooks|Webhooks and callbacks|3|webhook, callback, signature header, retry, delivery, event notification|
api-gateway|API gateway and proxy|2|api gateway, reverse proxy, nginx, envoy, haproxy, traefik, ingress, load balancer, kong|
cdn-caching|CDN and HTTP caching|3|cdn, cache control, etag, edge, cloudflare, fastly, purge, stale while revalidate|
dns|DNS|3|dns, resolver, nameserver, record, cname, ttl, zone, dnssec|
email|Email and messaging|3|email, smtp, imap, spf, dkim, dmarc, mailer, template, bounce|
sdk-client|SDK and client libraries|2|sdk, client library, wrapper, bindings, retry policy, pagination helper|
versioning|API versioning and compatibility|2|semver, breaking change, deprecation, backward compatible, version header, migration guide|
cors|CORS and browsers|3|cors, preflight, origin, same origin, credentials include|
serialization|Serialization formats|2|json, yaml, toml, xml, msgpack, avro, cbor, serialize, marshal, encoding|
file-upload|File uploads and storage|3|upload, multipart, mime type, presigned url, chunked, blob|
search-engine|Search and indexing|2|!elasticsearch, opensearch, lucene, full text, inverted index, tf idf, bm25, solr, ranking, relevance|
rag|Retrieval augmented generation|1|!rag, retrieval, embedding, chunk, vector, similarity, knowledge base, corpus, reranker, cosine|
`

const vocabData = `
relational-db|Relational databases|1|!postgres, postgresql, mysql, mariadb, sqlite, schema, table, foreign key, index, migration, transaction, orm|
nosql|NoSQL databases|2|!mongodb, dynamodb, cassandra, couchdb, document store, key value, wide column, redis|
caching|Caching|2|!cache, memcached, redis, ttl, eviction, lru, invalidation, cache hit, write through|
queue-messaging|Queues and messaging|2|!kafka, rabbitmq, sqs, nats, pubsub, message broker, queue, consumer, producer, topic, dead letter|
event-driven|Event driven architecture|2|event sourcing, cqrs, event bus, domain event, saga, outbox, eventual consistency|
stream-processing|Stream processing|3|stream processing, flink, kinesis, windowing, watermark, spark streaming|
data-pipeline|Data pipelines and ETL|2|!etl, elt, pipeline, airflow, dbt, ingestion, batch job, orchestrator, dag|
data-warehouse|Data warehouses and analytics|3|warehouse, snowflake, bigquery, redshift, olap, lakehouse, parquet, columnar, bi dashboard|
data-modeling|Data modeling|2|data model, entity relationship, normalization, schema design, erd, dimension table|
db-migrations|Database migrations|2|!migration, alembic, flyway, liquibase, goose, schema change, rollback, up down|
transactions|Transactions and consistency|2|transaction, isolation level, acid, deadlock, lock, optimistic locking, two phase commit, consistency|
backup-recovery|Backup and recovery|2|backup, restore, snapshot, point in time, disaster recovery, rpo, rto, replica|
graph-db|Graph databases|3|neo4j, graph database, cypher, node edge, traversal, knowledge graph|
timeseries|Time series data|3|time series, prometheus, influxdb, timescale, downsampling, retention, tsdb|
vector-db|Vector databases|2|vector database, pgvector, pinecone, qdrant, faiss, ann, hnsw, embedding index|
object-storage|Object storage|3|s3, bucket, object storage, blob storage, gcs, minio, presigned|
file-formats|File formats and parsing|3|parser, lexer, grammar, ast, csv, binary format, magic number, schema|
orm|ORMs and query builders|3|orm, gorm, sqlalchemy, prisma, hibernate, activerecord, sequelize, query builder|
data-quality|Data quality and governance|3|data quality, lineage, catalog, governance, schema registry, validation rules|
`

const vocabCloud = `
aws|Amazon Web Services|1|!aws, ec2, s3, lambda, iam, cloudformation, ecs, eks, rds, cloudwatch, dynamodb|\barn:aws[a-z-]*:
azure|Microsoft Azure|2|!azure, arm template, bicep, aks, entra, blob storage, azure devops, function app|
gcp|Google Cloud|2|!gcp, google cloud, gke, bigquery, cloud run, pubsub, gcloud, iam binding|
cloudflare|Cloudflare|3|!cloudflare, workers, durable object, r2, kv namespace, wrangler, pages|
serverless|Serverless|2|!serverless, lambda, function as a service, cold start, edge function, step functions|
terraform|Terraform and IaC|1|!terraform, !iac, infrastructure as code, hcl, module, provider, state file, tfvars, pulumi, cloudformation, ansible|\bresource\s+"[a-z0-9_]+"\s+"[^"]+"\s*\{
kubernetes|Kubernetes|1|!kubernetes, !k8s, kubectl, pod, deployment, helm, namespace, ingress, daemonset, statefulset, kustomize, operator, crd|\bapiVersion:\s*\S+[\s\S]{0,200}?\bkind:\s*[A-Z]
docker|Docker and containers|1|!docker, !dockerfile, container, image, compose, registry, layer, buildkit, podman, oci|(?m)^FROM\s+\S+
helm|Helm charts|3|helm, chart, values yaml, templating, release, tiller|
service-mesh|Service mesh|3|service mesh, istio, linkerd, sidecar, mtls, envoy|
networking|Networking|2|network, subnet, vpc, routing, nat, cidr, bgp, tcp, udp, packet, latency, mtu|
load-balancing|Load balancing|3|load balancer, round robin, sticky, health check, failover, anycast|
autoscaling|Autoscaling and capacity|3|autoscaling, hpa, capacity planning, scale out, scale in, quota, burst|
multi-cloud|Multi-cloud and portability|3|multi cloud, hybrid cloud, vendor lock in, portability, on prem|
finops|Cloud cost (FinOps)|3|finops, cost, billing, budget, reserved instance, spot, rightsizing, cost allocation|
virtualization|Virtualization|3|virtual machine, kvm, hypervisor, vmware, qemu, firecracker, vagrant|
edge-computing|Edge computing|3|edge, cdn worker, iot gateway, latency sensitive, regional|
config-management|Configuration management|2|ansible, chef, puppet, salt, playbook, idempotent, inventory, config drift|
secrets-infra|Infrastructure secrets|3|sealed secrets, external secrets, sops, vault agent, parameter store|
`

const vocabDelivery = `
ci-cd|CI/CD pipelines|1|!ci, !cd, pipeline, workflow, github actions, gitlab ci, jenkins, circleci, build matrix, artifact, runner, p:.github/workflows|(?m)^\s*(?:-\s+)?uses:\s*[\w./-]+@
release-mgmt|Release management|2|release, changelog, semver, tag, version bump, release notes, rollout, hotfix, backport|
deployment|Deployment strategies|2|deploy, blue green, canary, rolling update, rollback, feature flag, staging, production|
feature-flags|Feature flags|3|feature flag, toggle, launchdarkly, experiment, gradual rollout, kill switch|
build-systems|Build systems|2|!build, makefile, bazel, cmake, gradle, maven, justfile, ninja, compile, linker, cache|
package-mgmt|Package management|2|!package, dependency, npm, pip, cargo, go mod, gem, registry, lockfile, version constraint, vendor|
monorepo|Monorepos and workspaces|3|monorepo, workspace, nx, turborepo, lerna, pnpm workspace, go work|
git-workflow|Git and version control|1|!git, branch, commit, merge, rebase, pull request, cherry pick, tag, remote, worktree, stash|
code-review|Code review|2|code review, reviewer, approval, review comment, nit, lgtm, pull request, diff, codeowners|
dependency-updates|Dependency updates|2|dependabot, renovate, upgrade, bump, outdated, update policy, minor update, patch update|
containers-build|Container builds|3|multi stage build, buildx, image tag, base image, layer cache, kaniko, ko, jib|
artifact-registry|Artifact registries|3|artifact registry, ghcr, ecr, nexus, artifactory, proxy cache, publish|
gitops|GitOps|3|gitops, argocd, flux, reconcile, desired state, drift, sync|
pre-commit|Pre-commit and hooks|3|pre commit, git hook, husky, lint staged, commit msg, hook script|
conventional-commits|Commit conventions|3|conventional commits, commit message, feat, fix, chore, scope, breaking change footer|
branching|Branching strategies|3|trunk based, gitflow, release branch, long lived branch, feature branch, protected branch|
`

const vocabQuality = `
testing|Testing|1|!test, !tests, unit test, assert, mock, fixture, coverage, table driven, regression, flaky, golden file, snapshot|
integration-testing|Integration testing|2|integration test, testcontainers, contract test, pact, end to end, api test, test environment|
e2e-testing|End-to-end testing|2|!e2e, end to end, playwright, cypress, selenium, browser test, smoke test, puppeteer|
tdd|Test driven development|3|tdd, red green refactor, test first, bdd, gherkin, cucumber, spec|
property-testing|Property based testing|3|property based, quickcheck, hypothesis, rapid, generator, shrinking|
mutation-testing|Mutation testing|3|mutation testing, mutant, pit, stryker, survived|
code-coverage|Code coverage|2|coverage, codecov, lcov, cover profile, branch coverage, uncovered, threshold|
linting|Linting and formatting|2|lint, linter, eslint, golangci, ruff, flake8, prettier, gofmt, rustfmt, style guide|
static-typing|Type checking|3|type checker, mypy, pyright, tsc strict, type annotation, generics, inference|
code-quality|Code quality and refactoring|2|refactor, tech debt, code smell, complexity, cyclomatic, duplication, readability, maintainability|
test-data|Test data and fixtures|3|fixture, factory, seed data, faker, testdata, mock server, stub|
benchmarking|Benchmarking|2|benchmark, bench, ns op, throughput, p99, latency, load test, k6, wrk, profiling|
load-testing|Load and stress testing|3|load test, stress test, soak, locust, jmeter, gatling, capacity, saturation|
chaos|Chaos engineering|3|chaos, fault injection, game day, resilience, blast radius, chaos monkey|
qa-process|QA and test planning|3|test plan, test case, acceptance criteria, qa, regression suite, exploratory, bug report|
accessibility-test|Accessibility testing|3|axe, wcag test, screen reader test, a11y audit, lighthouse|
`

const vocabOps = `
observability|Observability|1|!observability, !metrics, !tracing, logging, opentelemetry, otel, span, dashboard, prometheus, grafana, instrumentation|
logging|Logging|2|!log, logger, structured logging, log level, slog, zap, logrus, syslog, journald, loki|
metrics|Metrics and monitoring|2|metric, counter, gauge, histogram, prometheus, statsd, datadog, alert rule, slo, sli|
tracing|Distributed tracing|2|trace, span, jaeger, zipkin, opentelemetry, trace id, propagation, sampling|
alerting|Alerting and on-call|2|alert, pager, oncall, pagerduty, opsgenie, escalation, runbook, threshold, noise|
sre|Site reliability|2|sre, slo, error budget, reliability, toil, availability, postmortem, mttr, capacity|
incident-mgmt|Incident management|2|incident, severity, outage, postmortem, rca, root cause, war room, status page, mitigation|
performance|Performance tuning|1|!performance, optimize, latency, throughput, profile, flame graph, hot path, allocation, bottleneck, cpu bound|
concurrency|Concurrency and parallelism|1|!concurrency, thread, mutex, lock, race condition, deadlock, atomic, goroutine, async, channel, semaphore|
memory-mgmt|Memory management|3|memory leak, garbage collector, gc pause, allocation, pool, heap profile, rss, oom|
scalability|Scalability|2|scale, sharding, partition, horizontal, replication, bottleneck, stateless, fan out|
resilience|Resilience patterns|2|retry, backoff, circuit breaker, timeout, bulkhead, fallback, idempotency, graceful degradation|
reliability-dr|Disaster recovery|3|disaster recovery, failover, multi region, rpo, rto, backup site, restore drill|
capacity|Capacity planning|3|capacity, headroom, forecast, utilization, saturation, limits, requests|
debugging|Debugging|2|debug, stack trace, breakpoint, delve, gdb, panic, crash, reproduce, bisect, core dump|
profiling|Profiling|3|pprof, profile, flame graph, perf, trace, cpu profile, heap profile, ebpf|
ebpf|eBPF and kernel tracing|3|ebpf, bpf, kprobe, tracepoint, bpftrace, xdp, perf event|
runbooks|Runbooks and operations|2|runbook, playbook, procedure, on call, checklist, step by step, operational|
sla-slo|Service levels|3|sla, slo, sli, error budget, uptime, availability target, burn rate|
`

const vocabAI = `
llm|Large language models|1|!llm, !prompt, completion, token, context window, model, inference, temperature, chat, system prompt, openai, anthropic, gemini|
agents|AI agents and tools|1|!agent, tool call, tool use, function calling, mcp, planner, subagent, orchestration, autonomous, harness|
embeddings|Embeddings and vectors|2|embedding, vector, cosine, similarity, dimension, encoder, sentence transformer|
fine-tuning|Fine-tuning and training|3|fine tune, finetune, lora, training, dataset, epoch, loss, gradient, rlhf, checkpoint|
ml-ops|MLOps|3|mlops, model registry, feature store, model serving, drift, retraining, mlflow, kubeflow|
ml-models|Machine learning models|2|machine learning, neural network, classifier, regression, tensor, pytorch, tensorflow, transformer, scikit, xgboost|
nlp|Natural language processing|3|nlp, tokenizer, named entity, sentiment, summarization, translation, bert, parsing text|
computer-vision|Computer vision|3|computer vision, image classification, object detection, yolo, ocr, segmentation, opencv|
speech|Speech and audio|3|speech, asr, tts, whisper, voice, audio, transcription, microphone, wake word|
prompt-engineering|Prompt engineering|2|prompt, few shot, chain of thought, system message, template, instruction, role play, delimiters|
evals|Model evaluation|2|eval, benchmark, accuracy, precision, recall, f1, judge, rubric, test set, hallucination|
guardrails|AI guardrails and safety|2|guardrail, moderation, safety filter, refusal, red team, alignment, policy classifier|
context-mgmt|Context management|2|context window, compaction, summarize, truncation, memory, prune, token budget, cache prefix|
model-routing|Model routing|3|router, fallback model, model selection, cost aware, provider, gateway, failover|
ai-coding|AI coding assistants|2|copilot, cursor, claude code, codegen, code completion, pair programming, agentic coding, plan mode|
decision-models|Decision and classifier models|3|classifier, decision, noul, score, threshold, probability, jev, sentinel|
`

const vocabFrontend = `
frontend|Frontend development|1|!frontend, component, dom, browser, css, html, render, state management, hydration, bundle|
react|React|2|!react, jsx, hook, usestate, useeffect, redux, next, props, virtual dom|
vue-svelte|Vue and Svelte|3|!vue, !svelte, pinia, nuxt, sveltekit, composition api, reactive|
angular|Angular|3|!angular, rxjs, ngmodule, directive, injectable, zone|
css-styling|CSS and styling|2|css, tailwind, sass, flexbox, grid, media query, style, theme, design token|
web-components|Web components|3|web component, custom element, shadow dom, lit, slot|
accessibility|Accessibility|2|!accessibility, !a11y, wcag, aria, screen reader, keyboard navigation, contrast, focus|
i18n|Internationalization|2|i18n, l10n, locale, translation, unicode, rtl, pluralization, gettext, icu|
design-system|Design systems and UI|2|design system, storybook, component library, tokens, figma, palette, typography|
bundlers|Bundlers and tooling|3|webpack, vite, rollup, esbuild, parcel, bundle size, tree shaking, minify, source map|
state-mgmt|State management|3|state management, redux, mobx, zustand, store, reducer, signal, context api|
browser-apis|Browser APIs|3|service worker, localstorage, indexeddb, fetch api, intersection observer, web worker, pwa|
web-perf|Web performance|2|core web vitals, lcp, cls, lazy load, prefetch, critical css, bundle, ttfb|
ssr|Server-side rendering|3|ssr, ssg, hydration, isr, static site, astro, remix, next js, edge render|
tui|Terminal user interfaces|2|!tui, terminal, bubble tea, lipgloss, ansi, cursor, keybinding, viewport, escape sequence, curses|
cli-design|Command line interfaces|2|!cli, flag, subcommand, argument, cobra, stdin, stdout, exit code, man page, completion|
desktop-apps|Desktop applications|3|electron, tauri, desktop app, gtk, qt, native window, tray|
games|Game development|3|game, unity, unreal, godot, sprite, physics, render loop, shader, ecs|
graphics|Graphics and rendering|3|opengl, vulkan, shader, texture, mesh, gpu, raytracing, rasterization|
ux-design|UX and product design|3|ux, wireframe, user flow, persona, usability, prototype, journey|
`

const vocabMobile = `
mobile|Mobile development|2|!mobile, ios, android, app store, play store, push notification, deep link, react native, flutter|
android|Android|3|!android, kotlin, gradle, apk, jetpack, activity, manifest, adb|
ios|iOS and macOS|3|!ios, macos, xcode, swiftui, cocoa, testflight, plist, app store connect|
embedded|Embedded and IoT|3|embedded, firmware, microcontroller, arduino, esp32, rtos, gpio, mqtt, sensor|
robotics|Robotics|3|robot, ros, actuator, kinematics, slam, lidar, motion planning|
hardware|Hardware and electronics|3|circuit, pcb, fpga, verilog, vhdl, schematic, voltage, spi, i2c|
os-internals|Operating systems|3|kernel, syscall, process, scheduler, filesystem, driver, linux, posix, signal, cgroup|
systemd-linux|Linux administration|2|systemd, journalctl, unit file, cron, sudo, package manager, apt, dnf, pacman, selinux|
windows|Windows|3|windows, powershell, registry, active directory, wsl, msi, group policy, winget|
macos|macOS|3|macos, homebrew, launchd, keychain, plist, gatekeeper, notarize|
bluetooth-wireless|Wireless and radio|3|bluetooth, ble, wifi, zigbee, lora, rf, antenna|
blockchain|Blockchain and crypto assets|3|blockchain, ethereum, bitcoin, wallet, smart contract, token, consensus, defi, nft|
`

const vocabDocs = `
documentation|Documentation|1|!documentation, readme, guide, tutorial, how to, reference, docs, manual, getting started, example|
architecture|Software architecture|1|!architecture, design, component, module, layer, boundary, coupling, diagram, pattern, microservice, monolith|
adr|Architecture decision records|2|!adr, decision record, context, decision, consequences, status accepted, alternatives considered, p:/adr/|
design-docs|Design documents and RFCs|2|rfc, design doc, proposal, goals, non goals, alternatives, tradeoff, open questions|
api-docs|API reference documentation|2|api reference, parameters, returns, example request, response schema, endpoint docs, godoc, javadoc|
changelog|Changelogs and release notes|2|changelog, release notes, added, changed, fixed, removed, unreleased, keep a changelog|
onboarding|Onboarding and contributing|2|contributing, onboarding, setup, development environment, first issue, code of conduct, pull request template|
troubleshooting|Troubleshooting guides|2|troubleshooting, faq, common errors, symptom, cause, fix, workaround, known issue|
tutorials|Tutorials and examples|2|tutorial, walkthrough, step by step, example, quickstart, sample, demo, exercise|
migration-guide|Migration guides|3|migration guide, upgrade, deprecated, breaking, from version, to version, compatibility|
specification|Specifications and standards|3|specification, rfc 2119, must, should, normative, conformance, standard, iso|
glossary|Glossaries and terminology|3|glossary, definition, terminology, acronym, abbreviation, term|
meeting-notes|Meeting notes and planning|3|meeting, agenda, action item, standup, retro, minutes, sprint, planning|
roadmap|Roadmaps and planning|3|roadmap, milestone, quarter, priority, backlog, epic, initiative, okr|
wiki|Knowledge base and wiki|3|wiki, knowledge base, confluence, notion, runbook index, faq|
diagrams|Diagrams and modeling|3|diagram, mermaid, plantuml, uml, sequence diagram, flowchart, c4, graphviz|
markdown|Markdown and writing|3|markdown, frontmatter, heading, table of contents, mdx, asciidoc, docs site|
docs-sites|Documentation sites|3|docusaurus, mkdocs, sphinx, hugo, jekyll, astro, static site generator|
style-guide|Style and conventions|2|style guide, convention, naming, formatting, idiom, best practice, guideline|
`

const vocabCompliance = `
compliance|Compliance and audit|1|!compliance, audit, control, evidence, soc 2, iso 27001, hipaa, pci dss, fedramp, gdpr, nist, framework|
gdpr-privacy|Privacy and GDPR|2|!gdpr, !privacy, data subject, consent, dpa, right to erasure, ccpa, data retention, pii, cookie banner|
licensing|Open source licensing|1|!license, licence, spdx, mit, apache, gpl, copyleft, notice, attribution, redistribution, sublicense|
soc2|SOC 2 and trust criteria|3|soc 2, trust services, availability, confidentiality, type ii, auditor, control owner|
pci|Payment card security|3|pci dss, cardholder, payment, tokenization, acquirer, saq|
hipaa|Healthcare data|3|hipaa, phi, covered entity, healthcare, ehr, hl7, fhir|
nist|NIST frameworks|3|nist, 800 53, 800 171, csf, rmf, ssdf, control family|
iso-27001|ISO 27001|3|iso 27001, isms, statement of applicability, annex a, risk treatment|
export-control|Export and legal|3|export control, sanctions, eccn, legal, terms of service, liability, indemnity|
ethics|AI ethics and responsible use|3|ethics, bias, fairness, transparency, responsible ai, human oversight, impact assessment|
risk-mgmt|Risk management|2|risk, likelihood, impact, mitigation, risk register, appetite, residual risk, exception|
policy|Policies and standards|2|policy, standard, procedure, exception, approval, governance, owner, review cycle|
sbom-compliance|SBOM and regulatory|3|executive order 14028, cra, cyber resilience act, ntia, minimum elements, attestation form|
security-practices|Secure development practices|2|secure sdlc, ssdf, security champion, secure by design, threat model, review gate, hardening|
data-residency|Data residency|3|data residency, sovereignty, region, cross border, schrems, localization|
accessibility-law|Accessibility compliance|3|ada, section 508, en 301 549, vpat, wcag conformance|
`

const vocabProduct = `
product|Product and business|3|product, customer, roadmap, user story, requirement, stakeholder, market, pricing, revenue, kpi|
requirements|Requirements and specifications|2|requirement, user story, acceptance criteria, use case, functional, non functional, scope, constraint|
project-mgmt|Project management|3|project, sprint, kanban, backlog, estimate, deadline, milestone, scrum, jira, issue tracker|
kanban|Kanban boards and work items|2|kanban, board, card, lane, wip limit, claim, handoff, work item, column|
agile|Agile and team process|3|agile, retro, standup, velocity, story points, scrum master, iteration|
metrics-product|Product analytics|3|analytics, funnel, retention, cohort, ab test, conversion, dau, event tracking|
support|Customer support|3|support ticket, helpdesk, escalation, sla, knowledge article, customer issue|
billing|Billing and payments|3|billing, invoice, subscription, stripe, payment, refund, plan, entitlement|
localization-biz|Market and localization|3|market, region, currency, tax, locale, regulation|
hiring-team|Team and hiring|3|hiring, interview, onboarding, performance review, career ladder, team topology|
open-source|Open source projects|2|open source, maintainer, contributor, issue triage, governance, fork, upstream, community|
legal-contracts|Contracts and legal text|3|agreement, clause, warranty, liability, term, confidential, licensee, jurisdiction|
`

const vocabCode = `
error-handling|Error handling|2|error, exception, panic, recover, wrap error, retry, sentinel error, result type, stack trace|
api-design|API and interface design|2|interface, contract, abstraction, backward compatible, public api, builder, option pattern, ergonomics|
design-patterns|Design patterns|2|pattern, factory, singleton, observer, strategy, decorator, adapter, dependency injection, repository pattern|
algorithms|Algorithms and data structures|2|algorithm, complexity, big o, sort, search, tree, graph, hash map, heap, dynamic programming, trie|
parsing|Parsing and compilers|2|parser, lexer, tokenizer, grammar, ast, compiler, interpreter, bytecode, codegen, regex|
regex|Regular expressions|3|regex, regular expression, pattern, capture group, lookahead, re2, match, quantifier|
text-processing|Text and Unicode|3|unicode, utf 8, normalization, grapheme, rune, encoding, bidi, control character, truncate|
date-time|Date and time|3|timezone, utc, timestamp, rfc 3339, duration, cron, calendar, leap second, clock|
file-io|Files and filesystems|2|file, path, directory, filesystem, symlink, permission bits, temp file, atomic rename, fsync, glob|
process-mgmt|Processes and subprocesses|2|process, subprocess, exec, signal, pid, stdin, stdout, pipe, daemon, supervisor, exit status|
config-settings|Configuration and settings|1|!config, !settings, configuration, option, flag, environment variable, yaml config, precedence, default, override, layer|
plugins|Plugins and extensions|2|plugin, extension, hook, manifest, extension point, marketplace, add on, middleware|
cli-tools|Command line tools|3|command, subcommand, flag, argument, stdout, help text, completion, shell integration|
code-generation|Code generation|3|generate, codegen, template, go generate, protoc, scaffolding, boilerplate, generator|
refactoring|Refactoring and migration|2|refactor, rename, extract, migrate, deprecate, legacy, rewrite, cleanup, strangler|
dependency-injection|Dependency injection|3|dependency injection, wire, container, constructor injection, inversion of control, provider|
state-machines|State machines and workflows|3|state machine, transition, workflow engine, temporal, fsm, statechart, saga|
scheduling|Scheduling and cron|3|schedule, cron, timer, ticker, job queue, periodic, at most once, debounce|
http-clients|HTTP clients|3|http client, retry, timeout, transport, connection pool, proxy, user agent, redirect|
functional|Functional programming|3|functional, immutable, pure function, closure, higher order, monad, currying, map reduce|
oop|Object oriented design|3|class, inheritance, polymorphism, encapsulation, solid, composition, interface, method|
generics|Generics and types|3|generic, type parameter, constraint, template, trait bound, covariance, union type|
interop|FFI and interoperability|3|ffi, cgo, bindings, jni, ctypes, interop, abi, shared library, wasm bridge|
logging-lib|Logging libraries|3|logger, slog, zap, logrus, winston, log4j, structured fields, log level|
serialization-lib|Encoding libraries|3|marshal, unmarshal, codec, gob, protobuf, msgpack, schema evolution|
caching-lib|In-process caching|3|lru, memoize, cache key, expire, singleflight, sync pool|
`

const vocabPlatform = `
agents-platform|Agent platform and fleet|2|worker, fleet, crew, agent profile, claim, lease, worktree, handoff, pass budget|
tools-registry|Tool registries and permissions|2|tool, registry, permission rule, allow rule, deny rule, ask gate, sandbox, surface, mode|
sessions|Sessions and transcripts|2|session, transcript, resume, jsonl, history, compaction, record, replay|
slash-commands|Slash commands and UI|3|slash command, palette, screen, keybinding, footer, panel, tab, prompt box|
skills|Skills and prompts|3|skill, prompt library, front matter, instruction, persona, template, system block|
mcp|Model Context Protocol|2|!mcp, model context protocol, mcp server, tool schema, stdio server, resource, prompt capability|
language-server|Language servers|3|lsp, language server, diagnostics, definition, references, hover, completion, workspace|
knowledge-index|Knowledge indexing|2|knowledge, index, chunk, retrieval, tf idf, similarity, ingest, label, tag, topic|
settings-layers|Settings layers|3|settings, layer, project layer, user layer, precedence, override, schema validation|
telemetry|Telemetry and usage|3|telemetry, usage, opentelemetry, counter, budget, tokens used, cost, quota|
sync|Synchronization|3|sync, mirror, upload, pull, conflict, merge, remote, last write wins|
auth-device|Device login and credentials|3|device login, credential file, keychain, token store, refresh token, login flow|
plugins-marketplace|Plugin marketplaces|3|marketplace, plugin install, manifest, namespace, listing, registry|
scripting|Scripting and automation|2|script, automation, cron job, batch, task runner, justfile, makefile target, glue|
`
