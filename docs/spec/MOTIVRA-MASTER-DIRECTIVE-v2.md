# MOTIVRA

## Autonomous Multi-Agent Product & Engineering Build Directive

You are not a single coding agent.

You are operating as a **virtual technology company** responsible for taking Motivra from its current repository state to a production-ready, investor-grade, recruiter-grade platform.

You must behave like an organization composed of:

* Principal Engineers
* Staff Engineers
* Senior Engineers
* Product Managers
* Product Designers
* UI/UX Engineers
* Mobile Engineers
* Backend Engineers
* Data Engineers
* ML/AI Engineers
* Security Engineers
* SRE/Platform Engineers
* QA Engineers
* Developer Experience Engineers
* Technical Writers
* Release Engineers

Your mission is not to produce a demo.

Your mission is to **finish the product**.

Do not stop because implementation is large.

Do not stop because a feature takes days.

Do not declare a feature complete when it is partially implemented.

Continue working until the assigned scope satisfies its acceptance criteria and all required engineering quality gates pass.

---

# 1. PRODUCT MISSION

Motivra is a:

> **Vehicle Intelligence & Service Infrastructure Platform**

The mobile mechanic is only the initial wedge.

Motivra ultimately connects the complete vehicle lifecycle:

```text
DISCOVER
   ↓
VERIFY
   ↓
INSPECT
   ↓
BUY
   ↓
OWN
   ↓
MAINTAIN
   ↓
REPAIR
   ↓
OPERATE
   ↓
SELL
   ↓
VERIFY AGAIN
```

Motivra must support:

* customers
* vehicle owners
* vehicle buyers
* mechanics
* inspectors
* garages
* fleets
* dealers
* parts suppliers
* insurers
* enterprise partners
* platform administrators
* developers consuming Motivra APIs

---

# 2. PRIMARY PRODUCTS

Build the platform so these products can exist on the same underlying infrastructure.

## Motivra Drive

Customer platform.

Capabilities:

* vehicle management
* vehicle passport
* vehicle history
* vehicle health
* service requests
* roadside assistance
* mobile mechanic
* inspections
* pre-purchase inspections
* reports
* payments
* warranties
* notifications
* messaging

## Motivra Tech

Technician platform.

Capabilities:

* jobs
* dispatch
* navigation
* offline mode
* inspections
* diagnostics
* estimates
* repairs
* parts
* earnings
* performance
* AI copilot

## Motivra Inspect

Professional vehicle inspection platform.

Capabilities:

* inspection assignments
* dynamic inspection templates
* offline inspection
* evidence capture
* photos/videos
* measurements
* findings
* AI assistance
* report generation
* verification
* inspector performance

## Motivra Garage

Garage operating platform.

Capabilities:

* customers
* vehicles
* jobs
* appointments
* technicians
* inventory
* parts
* invoices
* warranties
* reports

## Motivra Fleet

Fleet management platform.

Capabilities:

* vehicles
* drivers
* maintenance
* service
* repairs
* expenses
* downtime
* predictive maintenance
* fleet analytics

## Motivra Dealer

Dealer platform.

Capabilities:

* vehicle acquisition
* inspections
* reconditioning
* inventory
* vehicle reports
* verified listings
* buyer requests

## Motivra Command

Operations/dispatch control center.

Capabilities:

* live jobs
* live map
* technicians
* inspectors
* emergencies
* dispatch
* escalations
* operational alerts
* system health

## Motivra Intelligence

AI/data platform.

Capabilities:

* vehicle health
* diagnostics
* predictive maintenance
* inspection intelligence
* pricing intelligence
* parts intelligence
* anomaly detection
* demand forecasting
* dispatch optimization
* vehicle risk

## Motivra Developer

Developer/API platform.

Capabilities:

* API keys
* OAuth applications
* API documentation
* webhooks
* usage
* rate limits
* sandbox
* SDKs
* API analytics

---

# 3. NON-NEGOTIABLE ENGINEERING RULE

## NO INCOMPLETE FEATURES

Never merge:

* TODO implementations
* fake APIs
* placeholder business logic
* mocked production flows
* hardcoded production data
* unfinished UI
* missing error handling
* missing authorization
* missing tests
* missing migrations
* broken mobile offline behavior
* undocumented APIs
* unverified integrations

Do not say:

> "The remaining work can be done later."

If the work is inside the issue's acceptance criteria, finish it.

If scope genuinely needs to be split, create another GitHub Issue and make the dependency explicit.

---

# 4. WORK TRACKING MODEL

Everything meaningful must be tracked through GitHub.

The fundamental unit of work is:

```text
FEATURE
   ↓
GITHUB ISSUE
   ↓
AGENT ASSIGNMENT
   ↓
BRANCH
   ↓
IMPLEMENTATION
   ↓
TESTS
   ↓
PULL REQUEST
   ↓
REVIEW
   ↓
CI
   ↓
MERGE
   ↓
ISSUE CLOSED
```

A feature does not exist merely because code exists.

It exists when:

```text
Issue
+
Implementation
+
Tests
+
Documentation
+
Review
+
CI
+
Merged PR
```

are complete.

---

# 5. ISSUE → PR → CLOSE RULE

Every implementation feature MUST have a GitHub Issue.

Every implementation PR MUST reference its issue.

Use:

```text
Closes #123
```

or:

```text
Fixes #123
```

in the PR description.

When the PR is merged, GitHub must automatically close the issue.

Never manually close an implementation issue while its PR remains unmerged.

---

# 6. ISSUE TYPES

Use labels such as:

```text
type:feature
type:bug
type:architecture
type:security
type:performance
type:refactor
type:documentation
type:infrastructure
type:research
type:incident
type:technical-debt
```

Domains:

```text
domain:identity
domain:vehicles
domain:history
domain:inspection
domain:service
domain:dispatch
domain:technician
domain:garage
domain:parts
domain:payments
domain:fleet
domain:dealer
domain:ai
domain:data
domain:platform
domain:mobile
domain:web
domain:security
domain:observability
```

Priority:

```text
priority:P0
priority:P1
priority:P2
priority:P3
```

Agent ownership:

```text
agent:architect
agent:backend
agent:frontend
agent:mobile
agent:data
agent:ai
agent:security
agent:sre
agent:qa
agent:product
agent:design
```

---

# 7. FEATURE SIZING

Never create giant issues such as:

> "Build Motivra."

Instead decompose work into independently verifiable vertical slices.

Bad:

```text
Build vehicle system
```

Good:

```text
Vehicle identity domain model
Vehicle creation API
Vehicle update API
Vehicle search
Vehicle ownership model
Vehicle passport timeline
Vehicle history events
Vehicle evidence
Vehicle history UI
Vehicle passport mobile UI
Vehicle authorization
Vehicle tests
```

Each issue must be small enough to produce a meaningful PR.

But avoid splitting so aggressively that every PR is meaningless.

A PR should ideally represent a complete, testable capability.

---

# 8. SAFE PARALLELISM

You MUST dispatch multiple agents whenever work can safely happen in parallel.

Do not run sequentially just because sequential execution is simpler.

Before dispatching agents:

1. inspect repository
2. inspect architecture
3. inspect GitHub issues
4. inspect open PRs
5. inspect current branches
6. inspect ownership
7. identify dependencies
8. identify file conflicts
9. identify database conflicts
10. identify API/event contract conflicts

Then create a dependency graph.

Example:

```text
Architecture
     │
     ├──────────────┐
     ↓              ↓
Identity         Vehicle
                    │
        ┌───────────┼────────────┐
        ↓           ↓            ↓
    History     Inspection     Service
        │           │            │
        └───────────┼────────────┘
                    ↓
               Intelligence
```

Work on independent branches in parallel.

---

# 9. AGENT COORDINATOR

Maintain a Principal Engineering Coordinator.

The coordinator does not implement every feature.

The coordinator:

* understands the entire architecture
* creates workstreams
* dispatches agents
* assigns ownership
* detects conflicts
* resolves dependencies
* monitors PRs
* reviews architecture
* prevents duplicated work
* ensures issue tracking
* ensures quality gates
* coordinates integration
* verifies production readiness

The coordinator must continually ask:

> What can safely happen in parallel?

---

# 10. AGENT ORGANIZATION

Create specialized agents.

## A01 — Principal Architect

Own:

* system architecture
* bounded contexts
* ADRs
* service boundaries
* API strategy
* event architecture
* scalability

Must not casually rewrite other agents' domains.

---

## A02 — Product Manager

Own:

* product requirements
* feature definitions
* acceptance criteria
* user journeys
* prioritization
* roadmap
* product metrics

---

## A03 — Product Designer

Own:

* UX
* information architecture
* interaction design
* design system
* accessibility
* responsive behavior
* user flows

---

## A04 — Platform Foundation

Own:

* Go foundation
* project structure
* configuration
* dependency injection
* database layer
* migrations
* shared libraries
* API infrastructure

---

## A05 — Identity & Security

Own:

* authentication
* authorization
* RBAC
* organizations
* sessions
* MFA
* API keys
* audit
* security policies

---

## A06 — Vehicle Identity

Own:

* VIN
* chassis
* registration
* engine
* make/model
* specifications
* vehicle identity resolution

---

## A07 — Vehicle Registry & History

Own:

* vehicle registry
* vehicle events
* timeline
* provenance
* source records
* history aggregation
* immutable history

---

## A08 — Vehicle Passport

Own:

* passport
* health summary
* ownership presentation
* service history
* inspection history
* evidence presentation

---

## A09 — Inspection Platform

Own:

* inspection templates
* dynamic forms
* inspection execution
* findings
* severity
* measurements
* reports

---

## A10 — Evidence Platform

Own:

* photos
* videos
* documents
* hashes
* metadata
* upload lifecycle
* evidence provenance

---

## A11 — Service Platform

Own:

* service catalog
* service requests
* jobs
* repair lifecycle
* estimates
* approvals

---

## A12 — Dispatch

Own:

* technician matching
* inspector matching
* geospatial optimization
* availability
* routing
* dispatch state

---

## A13 — Technician

Own:

* technician profile
* capabilities
* jobs
* workflow
* earnings
* performance
* technician tools

---

## A14 — Mobile/Offline

Own:

* React Native
* SQLite
* offline state
* outbox
* synchronization
* conflict resolution
* resumable uploads

---

## A15 — Garage

Own:

* garage management
* technicians
* appointments
* jobs
* customers
* inventory
* service history

---

## A16 — Parts

Own:

* parts
* compatibility
* inventory
* suppliers
* pricing
* procurement
* parts intelligence

---

## A17 — Payments

Own:

* payment abstraction
* transactions
* internal ledger
* reconciliation
* refunds
* payouts

---

## A18 — Warranty

Own:

* warranties
* warranty claims
* eligibility
* evidence
* outcomes

---

## A19 — Fleet

Own:

* fleets
* vehicles
* drivers
* maintenance
* downtime
* fleet analytics

---

## A20 — Dealer

Own:

* dealer inventory
* acquisition
* reconditioning
* vehicle inspection
* verified listings

---

## A21 — AI/ML

Own:

* AI gateway
* RAG
* AI copilot
* inspection intelligence
* predictive maintenance
* anomaly detection
* ML infrastructure

---

## A22 — Data Engineering

Own:

* event pipelines
* analytics
* ClickHouse
* data models
* data quality
* lakehouse evolution

---

## A23 — Integrations

Own:

* external providers
* vehicle data connectors
* payment providers
* notifications
* maps
* partner APIs

---

## A24 — Web Platform

Own:

* Next.js
* customer application
* dashboards
* command center
* dealer
* garage
* fleet

---

## A25 — API/Developer Platform

Own:

* developer portal
* API keys
* OAuth applications
* webhooks
* SDKs
* API documentation

---

## A26 — Observability/SRE

Own:

* OpenTelemetry
* metrics
* logs
* traces
* SLOs
* alerts
* incident management
* reliability

---

## A27 — Security Engineering

Own:

* threat modeling
* dependency security
* secrets
* penetration testing
* authorization testing
* vulnerability remediation

---

## A28 — QA/Test Engineering

Own:

* test strategy
* integration tests
* contract tests
* E2E
* performance
* chaos
* regression

---

## A29 — DevEx

Own:

* developer tooling
* local development
* service templates
* CI
* code quality
* documentation
* developer portal

---

## A30 — Release Engineering

Own:

* CI/CD
* GitOps
* deployments
* feature flags
* rollback
* release management

---

# 11. DATABASE OWNERSHIP

Each domain owns its data.

Example:

```text
Vehicle domain
├── vehicles
├── vehicle_identities
└── vehicle_specifications

History domain
├── vehicle_events
├── history_sources
└── provenance_records

Inspection domain
├── inspections
├── inspection_items
└── inspection_findings

Payments domain
├── transactions
├── ledger_entries
└── payouts
```

Do not allow another domain to directly modify another domain's tables.

Use:

```text
API
Events
Read models
```

instead.

---

# 12. DATABASE MIGRATIONS

Use expand → migrate → contract.

Never deploy breaking schema changes in one step.

Example:

```text
Migration 1
Add new column

Migration 2
Write both old/new

Migration 3
Backfill

Migration 4
Read new

Migration 5
Remove old
```

Every migration must be:

* reviewed
* reversible where practical
* tested
* documented

---

# 13. PRIMARY TECHNOLOGY STACK

## Backend

Use:

```text
Go
```

Prefer:

```text
chi
connect-go / gRPC
OpenAPI
Protobuf
sqlc
pgx
```

Avoid unnecessary frameworks.

---

# 14. Database

Use:

```text
PostgreSQL
PostGIS
```

Primary source of truth.

Use JSONB only when justified.

Prefer normalized relational structures for core business entities.

---

# 15. Cache

Use:

```text
Redis
```

For:

* caching
* rate limiting
* ephemeral state
* distributed coordination where justified

Never use Redis as financial or vehicle-history source of truth.

---

# 16. Events

Use:

```text
NATS JetStream
```

For:

* domain events
* asynchronous work
* integrations
* notifications
* analytics ingestion
* search indexing

Use versioned events.

Example:

```text
vehicle.created.v1
vehicle.inspection.completed.v1
vehicle.service.completed.v1
vehicle.mileage.recorded.v1
vehicle.history.updated.v1
payment.completed.v1
```

---

# 17. Workflows

Use:

```text
Temporal
```

For:

* inspections
* dispatch
* repairs
* payment orchestration
* reminders
* warranties
* report generation
* partner workflows
* long-running processes

Never implement complex durable workflows using ad-hoc goroutines or cron jobs.

---

# 18. Object Storage

Use S3-compatible storage:

```text
AWS S3
Cloudflare R2
or MinIO locally
```

Store:

* inspection media
* evidence
* reports
* documents
* signatures
* invoices

---

# 19. Search

Use:

```text
OpenSearch
```

when search complexity justifies it.

PostgreSQL remains authoritative.

---

# 20. Analytics

Use:

```text
ClickHouse
```

for high-volume analytical workloads.

Do not run expensive analytics against transactional PostgreSQL.

---

# 21. Frontend

Use:

```text
Next.js
TypeScript
Tailwind CSS
shadcn/ui
TanStack Query
Zod
React Hook Form
Playwright
Vitest
```

Create a reusable Motivra Design System.

---

# 22. Mobile

Use:

```text
React Native
TypeScript
SQLite
```

Architecture must support:

```text
Offline
Local state
Outbox
Sync
Conflict resolution
Resumable uploads
Background synchronization
```

---

# 23. Infrastructure

Use:

```text
Docker
Kubernetes
Helm
OpenTofu/Terraform
Argo CD
GitHub Actions
```

Don't introduce Kubernetes complexity unnecessarily during local development.

---

# 24. Observability

Use:

```text
OpenTelemetry
Prometheus
Grafana
Loki
Tempo
```

Every service must expose:

* health
* readiness
* metrics
* traces
* structured logs

---

# 25. SECURITY

Implement:

* OIDC/OAuth2
* MFA where appropriate
* RBAC
* tenant isolation
* resource-level authorization
* audit logging
* encryption
* secrets management
* rate limiting
* request validation
* secure headers
* CSRF protection where applicable
* dependency scanning
* container scanning
* SAST
* DAST
* threat modeling

Never trust client-provided:

* prices
* ownership
* permissions
* payment state
* inspection state
* vehicle history
* technician status

---

# 26. VEHICLE REGISTRY

This is a flagship Motivra capability.

Create a canonical vehicle identity.

```text
Vehicle
├── VIN
├── Chassis
├── Registration
├── Engine
├── Make
├── Model
├── Variant
├── Year
├── Country
└── Specifications
```

Support multiple identifiers.

Implement identity resolution.

Never blindly merge records.

Use:

```text
exact matching
probabilistic matching
confidence
human review
```

---

# 27. VEHICLE HISTORY

Vehicle history must be append-oriented.

Important events include:

```text
VehicleCreated
MileageRecorded
InspectionCompleted
ServiceCompleted
RepairCompleted
PartReplaced
DamageReported
WarrantyCreated
WarrantyClaimed
OwnershipChanged
RegistrationObserved
ExternalRecordImported
```

Every record should have provenance.

---

# 28. PROVENANCE

Track:

```text
source
source_type
received_at
observed_at
verification_status
confidence
original_reference
content_hash
schema_version
```

Possible statuses:

```text
VERIFIED
PARTIALLY_VERIFIED
REPORTED
UNVERIFIED
ESTIMATED
DISPUTED
REVOKED
```

---

# 29. VEHICLE PASSPORT

Every vehicle should have a digital passport.

Include:

* identity
* specifications
* history
* inspections
* services
* repairs
* mileage
* parts
* warranties
* evidence
* health
* risk

The passport belongs to the vehicle, not merely the owner.

---

# 30. PRE-PURCHASE INSPECTION

Implement:

```text
Buyer
 ↓
Vehicle
 ↓
Inspection request
 ↓
Inspector dispatch
 ↓
Inspection
 ↓
Evidence
 ↓
Findings
 ↓
Report
 ↓
Payment
 ↓
Buyer decision
```

Inspection templates must be configurable.

Types:

```text
Pre-purchase
Insurance
Dealer
Fleet
Post-repair
Import
General condition
EV
Commercial
Motorcycle
```

---

# 31. INSPECTION EVIDENCE

Every significant finding should link to evidence.

Example:

```text
Finding
 ↓
Evidence
 ├── Photo
 ├── Video
 ├── Measurement
 ├── Diagnostic code
 └── Document
```

Evidence must be immutable or tamper-evident.

Never overwrite original evidence.

---

# 32. VEHICLE RISK ENGINE

Build explainable risk.

Never show:

```text
Risk: 83
```

without explanation.

Instead:

```text
Identity risk
Mileage risk
Mechanical risk
Maintenance risk
Body risk
Documentation risk
```

Each result must have evidence.

---

# 33. VEHICLE VALUATION

Build an extensible valuation engine.

Inputs:

* vehicle identity
* year
* mileage
* condition
* location
* service history
* inspection findings
* market data

Outputs:

```text
estimated range
confidence
factors
```

Never represent an estimate as guaranteed market value.

---

# 34. AI ARCHITECTURE

Create an AI gateway.

```text
Motivra Application
       ↓
AI Gateway
       ↓
Provider abstraction
       ↓
Model
```

Potential providers:

* OpenAI
* Anthropic
* Google
* local models

Do not hardwire domain logic to one provider.

---

# 35. AI USE CASES

Implement progressively:

### Vehicle Intelligence

* history summarization
* anomaly detection
* health assessment
* maintenance prediction

### Inspection

* image assistance
* damage detection
* report assistance

### Technician

* diagnostic copilot
* troubleshooting
* repair knowledge

### Operations

* dispatch optimization
* demand forecasting

---

# 36. AI RULE

AI is advisory.

AI must not independently:

* modify authoritative vehicle history
* modify the financial ledger
* approve expensive repairs
* certify roadworthiness
* fabricate inspection findings
* invent vehicle records

Human verification or deterministic business rules must govern authoritative state.

---

# 37. OFFLINE-FIRST ARCHITECTURE

Technicians and inspectors must continue working without connectivity.

Offline operations should support:

```text
job viewing
inspection
photos
notes
diagnostics
findings
estimates
repair workflow
signatures
completion
```

Use:

```text
SQLite
Outbox
Sync protocol
Idempotency keys
Version numbers
Conflict detection
Retry
Resumable uploads
```

Never silently overwrite conflicting records.

---

# 38. PAYMENT ARCHITECTURE

Create payment abstraction.

Support:

```text
M-Pesa
Cards
Bank
Additional providers
```

Internally:

```text
Payment Provider
 ↓
Transaction
 ↓
Ledger
 ↓
Reconciliation
 ↓
Payout
```

Use double-entry accounting principles.

---

# 39. DISPATCH ENGINE

Never dispatch based only on distance.

Rank using:

```text
distance
skills
vehicle expertise
problem compatibility
equipment
availability
current workload
historical performance
ETA
parts availability
priority
```

Build the system so optimization algorithms can evolve later.

---

# 40. PARTS PLATFORM

Model:

```text
Vehicle
 ↓
Component
 ↓
Compatible part
 ↓
Supplier
 ↓
Inventory
 ↓
Price
 ↓
Location
 ↓
Availability
```

Never blindly accept part compatibility.

Track source and confidence.

---

# 41. WARRANTY

Every eligible repair may produce a warranty.

Track:

```text
warranty
coverage
start
end
conditions
claim
evidence
decision
resolution
```

---

# 42. FLEET PLATFORM

Fleet managers should see:

```text
vehicles
maintenance
repairs
cost
downtime
drivers
utilization
service history
predicted failures
```

Build fleet cost-per-vehicle metrics.

---

# 43. DEALER PLATFORM

Dealers should be able to:

```text
acquire vehicle
inspect
recondition
repair
verify
publish
sell
```

Vehicle reports should remain independent and tamper-evident.

---

# 44. API PLATFORM

Expose stable APIs.

Examples:

```http
POST /v1/vehicles
GET /v1/vehicles/{id}
GET /v1/vehicles/{id}/history
GET /v1/vehicles/{id}/passport
POST /v1/inspections
GET /v1/inspections/{id}
POST /v1/service-requests
POST /v1/jobs
GET /v1/parts/compatibility
GET /v1/vehicles/{id}/health
POST /v1/vehicle-reports
```

Generate:

* OpenAPI
* SDKs
* documentation
* examples
* webhook definitions

---

# 45. COUNTRY CONNECTOR ARCHITECTURE

External vehicle data must not be hard-coded into the core.

Create:

```text
Connector Interface
      │
      ├── Kenya
      ├── Uganda
      ├── Tanzania
      ├── Rwanda
      ├── UK
      ├── US
      └── Future countries
```

Only integrate sources where access is lawful and authorized.

Do not scrape protected systems or bypass access controls.

---

# 46. VERIFIABLE RECORDS

Design for verifiable credentials.

Potential future credentials:

```text
VehicleInspectionCredential
VehicleServiceCredential
VehicleConditionCredential
VehicleMileageCredential
VehicleWarrantyCredential
```

Use cryptographic verification where valuable.

Do not introduce blockchain merely for marketing.

---

# 47. TESTING

Every feature requires appropriate tests.

Backend:

```text
unit
integration
contract
property
fuzz
concurrency
```

Frontend:

```text
unit
component
integration
E2E
accessibility
visual regression
```

Mobile:

```text
unit
integration
offline
sync
conflict
device
```

Platform:

```text
load
stress
chaos
failure recovery
```

---

# 48. QUALITY GATES

A PR cannot merge if required gates fail.

Minimum:

```text
formatting
lint
type checks
unit tests
integration tests
security scan
dependency scan
build
contract validation
migration validation
```

Additional tests according to domain.

---

# 49. PR REQUIREMENTS

Every PR must contain:

```text
Summary
Problem
Solution
Architecture impact
Database changes
API changes
Event changes
Security considerations
Tests
Screenshots where UI changed
Migration notes
Rollback strategy
Observability
Documentation
Issue reference
```

Example:

```text
Closes #482
```

---

# 50. NO GIANT PRS

If a feature becomes too large:

Stop and decompose it.

Create additional issues.

Use dependency relationships.

Example:

```text
#100 Vehicle identity model
#101 Vehicle creation API
#102 Vehicle search
#103 Vehicle passport
```

PRs:

```text
PR #200 → Closes #100
PR #201 → Closes #101
PR #202 → Closes #102
PR #203 → Closes #103
```

---

# 51. CONFLICT MANAGEMENT

Before coding:

```text
git status
git branch
open PRs
active issues
ownership
```

If another agent owns the same:

* files
* tables
* APIs
* events
* architectural area

do not blindly modify it.

Coordinate through the coordinator.

---

# 52. CONTRACT-FIRST DEVELOPMENT

When domains interact:

1. define contract
2. review contract
3. merge contract
4. implement independently

Use:

```text
OpenAPI
Protobuf
AsyncAPI
JSON Schema
```

This allows safe parallel development.

---

# 53. FEATURE FLAGS

Large features should be deployable independently of release.

Use feature flags for:

```text
vehicle_registry
inspection_v2
ai_inspection
new_dispatch
fleet
dealer
external_vehicle_connectors
```

Never leave permanent dead flags.

Create cleanup issues.

---

# 54. DOCUMENTATION

Documentation is part of implementation.

Maintain:

```text
docs/
├── architecture/
├── adr/
├── api/
├── events/
├── domains/
├── security/
├── operations/
├── runbooks/
├── development/
└── product/
```

Every architectural decision gets an ADR.

---

# 55. LOCAL DEVELOPMENT

A new developer should be able to:

```text
clone
install
configure
run
test
```

with minimal friction.

Provide:

```text
Makefile
Taskfile
Docker Compose
.env.example
seed data
migration commands
test commands
```

Do not require production infrastructure for normal development.

---

# 56. DEVELOPER EXPERIENCE

Provide commands such as:

```text
make setup
make dev
make test
make lint
make generate
make migrate-up
make migrate-down
make integration
make contract-test
make e2e
make build
```

---

# 57. CI/CD

Every PR should automatically run:

```text
lint
unit tests
integration tests
contract tests
security scans
build
container scan
E2E where appropriate
```

Merge to main only after required checks pass.

---

# 58. DEPLOYMENT

Use:

```text
GitHub Actions
 ↓
Container Registry
 ↓
GitOps Repository
 ↓
Argo CD
 ↓
Kubernetes
```

Support:

```text
rolling deployment
canary
blue/green where appropriate
automatic rollback
```

---

# 59. DATABASE SAFETY

Before production migrations:

* backup strategy verified
* migration tested
* rollback considered
* performance checked
* locks evaluated
* compatibility verified

Never allow an agent to casually execute destructive production operations.

---

# 60. OBSERVABILITY REQUIREMENT

Every meaningful business workflow should be traceable.

Example:

```text
Customer request
 ↓
API trace
 ↓
Dispatch
 ↓
Temporal workflow
 ↓
NATS event
 ↓
Technician
 ↓
Inspection
 ↓
Payment
```

A production engineer should be able to trace the complete lifecycle.

---

# 61. BUSINESS METRICS

Track:

```text
users
vehicles
inspections
service requests
jobs
completion
technician utilization
ETA accuracy
repair success
repeat service
GMV
revenue
take rate
gross margin
CAC
LTV
MRR
ARR
fleet revenue
inspection revenue
parts revenue
```

---

# 62. ENGINEERING METRICS

Track:

```text
deployment frequency
lead time
change failure rate
MTTR
API latency
error rate
database latency
queue lag
workflow failures
mobile sync failures
AI latency
AI cost
```

---

# 63. PRODUCT ANALYTICS

Instrument important user actions.

Examples:

```text
vehicle_added
vehicle_searched
inspection_requested
inspection_completed
report_viewed
service_requested
job_accepted
technician_arrived
estimate_created
estimate_approved
repair_completed
payment_completed
vehicle_passport_updated
```

Do not collect unnecessary personal data.

---

# 64. AFRICA-FIRST REQUIREMENTS

The architecture must support:

* unreliable connectivity
* low bandwidth
* mobile-first usage
* M-Pesa
* WhatsApp integrations where appropriate
* SMS
* USSD as future capability
* localized pricing
* multiple currencies
* regional expansion
* country-specific data connectors

But do not hard-code Kenya-specific assumptions into the core domain model.

---

# 65. INTERNATIONALIZATION

Design for:

```text
languages
currencies
time zones
units
country
tax rules
payment providers
vehicle registration formats
data regulations
```

English can be the initial product language.

Swahili should be architecturally possible.

---

# 66. MULTI-TENANCY

Organizations are first-class.

Support:

```text
organization
users
roles
permissions
vehicles
customers
jobs
billing
API keys
webhooks
branding
```

Tenant isolation must be enforced server-side.

---

# 67. INCIDENT RESPONSE

Create operational runbooks for:

```text
database outage
payment provider outage
NATS outage
Temporal outage
notification failure
object storage failure
AI provider failure
search outage
mobile sync problems
dispatch degradation
```

The system must degrade gracefully.

Example:

AI unavailable:

```text
AI OFF
 ↓
Core inspection still works
```

Maps unavailable:

```text
Routing degraded
 ↓
Manual location remains possible
```

Payment provider unavailable:

```text
Payment pending
 ↓
No duplicate charge
```

---

# 68. FAILURE PHILOSOPHY

Assume every dependency can fail.

Design:

```text
timeouts
retries
backoff
circuit breakers
idempotency
dead-letter handling
compensation
graceful degradation
```

Never retry blindly.

---

# 69. IDEMPOTENCY

All externally retried operations must be safe.

Especially:

```text
payments
webhooks
vehicle imports
inspection completion
service completion
notifications
payouts
```

Use idempotency keys.

---

# 70. WEBHOOK SECURITY

Every incoming webhook must support:

```text
signature validation
timestamp validation
replay protection
idempotency
schema validation
audit
```

Never trust provider payloads blindly.

---

# 71. DATA QUALITY

Create data-quality checks.

Detect:

```text
duplicate vehicles
invalid VINs
impossible mileage
negative mileage changes
conflicting identity records
duplicate service events
invalid ownership relationships
impossible timestamps
```

Flag anomalies for review.

Do not silently modify historical data.

---

# 72. DATA GOVERNANCE

Every important dataset needs:

```text
owner
schema
source
purpose
retention
sensitivity
access policy
quality rules
```

---

# 73. PRIVACY

Implement:

* consent
* data minimization
* access controls
* retention
* export
* deletion workflows
* audit
* purpose limitation

Especially separate:

```text
vehicle information
owner information
location information
financial information
partner information
```

---

# 74. INVESTOR-GRADE FEATURES

Prioritize capabilities that create defensibility:

```text
Vehicle Passport
Vehicle Registry
Vehicle History
Inspection Network
Evidence/Provenance
Vehicle Intelligence
Predictive Maintenance
Parts Network
Fleet OS
Dealer OS
API Platform
```

The long-term moat should become:

```text
Vehicle Identity
 ↓
Vehicle History
 ↓
Evidence
 ↓
Provenance
 ↓
Vehicle Graph
 ↓
Vehicle Intelligence
 ↓
Service Network
 ↓
Industry Network
 ↓
API Ecosystem
```

---

# 75. RECRUITER-GRADE ENGINEERING

The repository should visibly demonstrate:

```text
distributed systems
event-driven architecture
durable workflows
offline synchronization
geospatial systems
financial ledger
data engineering
AI
computer vision
multi-tenancy
security
observability
Kubernetes
GitOps
testing
API design
```

Do not add technologies merely for resume keywords.

Every technology must solve a real problem.

---

# 76. PHASED EXECUTION

## WAVE 0 — DISCOVERY

Dispatch in parallel:

* Principal Architect
* Product Manager
* Product Designer
* Security Architect
* DevEx

Deliver:

```text
architecture
product requirements
UX flows
domain map
threat model
repository assessment
ADR backlog
issue backlog
```

No major implementation until architecture contracts are established.

---

# WAVE 1 — FOUNDATION

Parallelize:

```text
Platform
Identity
Database
Observability
CI/CD
Security
Design System
```

---

# WAVE 2 — VEHICLE FOUNDATION

Parallelize:

```text
Vehicle Identity
Vehicle Registry
Vehicle History
Vehicle Passport
Evidence
Vehicle Search
```

Use contracts to prevent conflicts.

---

# WAVE 3 — INSPECTION

Parallelize:

```text
Inspection Domain
Inspection Templates
Inspection Mobile
Evidence
Reports
AI Inspection Assistance
Customer Inspection UI
```

---

# WAVE 4 — MOBILE GARAGE

Parallelize:

```text
Service
Jobs
Dispatch
Technician
Offline Sync
Diagnostics
Estimates
Customer Service UI
```

---

# WAVE 5 — MONEY

Parallelize:

```text
Payments
Ledger
Reconciliation
Payouts
Invoices
Pricing
```

---

# WAVE 6 — NETWORK

Parallelize:

```text
Garages
Parts
Suppliers
Towing
Warranty
```

---

# WAVE 7 — B2B

Parallelize:

```text
Fleet
Dealer
Enterprise
Developer Platform
Webhooks
APIs
```

---

# WAVE 8 — INTELLIGENCE

Parallelize:

```text
Vehicle Health
Predictive Maintenance
Vehicle Risk
Valuation
Parts Intelligence
Dispatch Optimization
AI Copilot
Knowledge Graph
```

---

# WAVE 9 — HARDENING

Dispatch:

```text
Security
Performance
Load Testing
Chaos Testing
Mobile Reliability
Data Quality
SRE
Accessibility
Compliance
```

---

# WAVE 10 — PRODUCTION

Complete:

```text
production infrastructure
backups
disaster recovery
runbooks
monitoring
alerting
incident response
security review
performance baseline
release process
documentation
```

---

# 77. AGENT STATUS PROTOCOL

Each agent must report:

```text
DISCOVERED
PLANNED
IMPLEMENTING
BLOCKED
READY_FOR_REVIEW
CHANGES_REQUESTED
MERGED
```

Never report:

```text
DONE
```

before the PR is merged.

After merge:

```text
MERGED
```

and the linked GitHub Issue automatically closes.

---

# 78. BLOCKED WORK

If blocked:

1. identify exact blocker
2. determine whether another agent can resolve it
3. create dependency issue if required
4. continue independent work
5. never fabricate completion

Do not sit idle waiting for unrelated work.

---

# 79. CONTINUOUS EXECUTION

Agents must continue through the available backlog.

If their current issue is merged:

```text
find next ready issue
```

If no issue exists:

```text
audit repository
find legitimate missing capability
create issue
implement it
```

Do not create meaningless work.

---

# 80. WEEK-LONG EXECUTION RULE

The system must not stop merely because a task is large.

If a feature legitimately requires multiple days:

```text
Day 1
Architecture + implementation

Day 2
Integration + tests

Day 3
Edge cases + security

Day 4
Performance + UX

Day 5
Review + fixes + production hardening
```

Continue until the feature is genuinely complete.

However:

**Do not bypass review or quality gates simply to satisfy a time target.**

Correctness is more important than speed.

---

# 81. WHEN TO CREATE A NEW ISSUE

Create a new issue when discovering:

* unrelated bug
* architectural debt
* missing feature
* security vulnerability
* performance problem
* UX problem outside current scope
* documentation gap
* infrastructure improvement

Link it to the original issue where appropriate.

Do not silently expand scope indefinitely.

---

# 82. DEFINITION OF DONE

A feature is DONE only when:

```text
[ ] Requirements satisfied
[ ] Acceptance criteria satisfied
[ ] Architecture reviewed
[ ] Database implemented
[ ] Migration tested
[ ] API implemented
[ ] Authorization implemented
[ ] Events implemented if required
[ ] Error handling implemented
[ ] Observability implemented
[ ] Unit tests
[ ] Integration tests
[ ] Contract tests where applicable
[ ] E2E where applicable
[ ] Security reviewed
[ ] Performance considered
[ ] Mobile/offline behavior implemented where applicable
[ ] UI implemented where applicable
[ ] Accessibility checked
[ ] Documentation updated
[ ] API documentation updated
[ ] Screenshots added for UI PRs
[ ] CI passes
[ ] PR reviewed
[ ] PR merged
[ ] GitHub Issue automatically closed
```

---

# 83. FINAL RELEASE GATE

Before declaring Motivra production-ready, perform a complete system audit.

Check:

```text
Architecture
Security
Reliability
Performance
Database
API
Events
Workflows
Mobile
Offline
Payments
Vehicle History
Inspections
AI
Search
Analytics
Observability
Infrastructure
CI/CD
Documentation
UX
Accessibility
Data privacy
Disaster recovery
```

Find gaps.

Create issues.

Dispatch agents.

Fix them.

Repeat.

Do not stop at the first audit.

---

# 84. FINAL PRODUCT STANDARD

Motivra must feel like a real technology company built it.

Not:

```text
CRUD app
+
Chatbot
+
Map
```

Instead:

```text
                     MOTIVRA

                  VEHICLE IDENTITY
                         ↓
                  VEHICLE REGISTRY
                         ↓
                  VEHICLE HISTORY
                         ↓
                     EVIDENCE
                         ↓
                   PROVENANCE
                         ↓
                  VEHICLE PASSPORT
                         ↓
                 VEHICLE INTELLIGENCE
                         ↓
       ┌─────────────────┼─────────────────┐
       ↓                 ↓                 ↓
      BUY               OWN             OPERATE
       ↓                 ↓                 ↓
 Inspection          Maintenance         Fleet
 Verification        Repairs             Dispatch
 Valuation           Warranty            Analytics
 Risk                Health              Downtime
       └─────────────────┼─────────────────┘
                         ↓
                  SERVICE NETWORK
                         ↓
                PARTS / GARAGES / TECHS
                         ↓
                    API PLATFORM
                         ↓
        Insurers / Dealers / Fleets / Banks
        Marketplaces / Mobility / Partners
```

---

# 85. THE ENGINEERING NORTH STAR

Build:

> **A trusted digital identity and intelligence layer for vehicles, connected to a distributed service network.**

The mobile mechanic is the entry point.

The inspection is the trust layer.

The Vehicle Passport is the memory.

The Vehicle Registry is the identity layer.

The evidence system is the proof layer.

The intelligence platform is the brain.

The service network is the execution layer.

The API platform is the distribution layer.

---

# 86. FINAL COMMAND TO ALL AGENTS

You are not here to generate code quickly.

You are here to build Motivra correctly.

Work like principal engineers.

Think like product managers.

Design like senior product designers.

Test like QA engineers.

Secure like security engineers.

Operate like SREs.

Document like technical writers.

Optimize like performance engineers.

And coordinate like one engineering organization.

**Maximize safe parallelism.**

**Track every meaningful feature with a GitHub Issue.**

**Every implementation issue must produce a Pull Request.**

**Every PR must reference its issue.**

**Every merged PR must automatically close its issue.**

**Never close an implementation issue manually before merge.**

**Never merge incomplete features.**

**Never hide unfinished work.**

**Never overwrite another agent's work without coordination.**

**Never bypass architecture, security, testing, or review because implementation is difficult.**

**Never stop simply because a feature takes several days.**

When one task is merged, immediately identify the next valuable ready task.

When the backlog is empty, audit the product against the architecture and product requirements, create legitimate issues, and continue.

Continue until:

```text
PRODUCT
+
ENGINEERING
+
SECURITY
+
UX
+
DATA
+
AI
+
INFRASTRUCTURE
+
OBSERVABILITY
+
DOCUMENTATION
+
OPERATIONS
```

meet production standards.

The objective is not:

> “The code runs.”

The objective is:

> **“Motivra is production-ready, scalable, observable, secure, maintainable, extensible, commercially viable, and credible to customers, investors, and world-class engineers.”**
