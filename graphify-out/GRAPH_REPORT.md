# Graph Report - k8s-agent-sandbox  (2026-11-15)

## Corpus Check
- 47 files · ~68,047 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 2051 nodes · 4106 edges · 150 communities (125 shown, 25 thin omitted)
- Extraction: 95% EXTRACTED · 5% INFERRED · 0% AMBIGUOUS · INFERRED: 216 edges (avg confidence: 0.81)
- Token cost: 215,010 input · 0 output

## Community Hubs (Navigation)
- Package Files And Imports
- Setup Script And Skill Docs
- CRD Types And Deepcopy
- Coordinator And Remediation Design
- Incident CRD Schema
- Remediation CRD Schema
- TriageRun CRD Schema
- Coordinator Reconciler
- MCP Toolbox And OpenAI Translation
- RemediationSkill CRD Schema
- Remediation Namespace RBAC
- Crashloop Demo Workload
- Remediation Executor
- gVisor Smoke Test
- TriageSkill CRD Schema
- Egress Network Policies
- Triage Server Tests
- SigNoz Foundry Casting
- Triage Agent API
- k8s-infra Collector Values
- Agent Authentication
- Agent Loop
- README And Working Agreements
- Coordinator Test Harness
- Remediation Policy And Targets
- Admission Policy Rules
- Triage Network Policies
- Coordinator Manifests
- Sandbox Launcher
- Agent Tests
- Binary Env Config
- Coordinator Deployment
- Egress Gateway Config
- Triage Controller Deployment
- Executor And Remediate Tests
- Triage Agent Binary
- Triage Namespaces
- Triage Controller RBAC
- Lean Collector Values
- K8s MCP Identity
- Kind Cluster Config
- Lean Kind Cluster
- Starter RemediationSkill
- Run Notifications
- Report Parsing
- skills Task
- installation fields
- coordinator ServiceAccount coordinator
- triage fields
- actions Kube
- main TokenFileTransport
- triage RoleBinding triage-controller-intake
- ? TestCheckAllowsPodBoundServiceAccount()
- claude Claude
- alert Webhook
- egress Deployment egress-gateway
- k8s-mcp ClusterRoleBinding triage-k8s-readonly
- k8s-mcp fields
- llm-proxy Service llm-proxy
- ? Identity
- coordinator RoleBinding coordinator
- egress fields
- llm-proxy ClusterRoleBinding llm-proxy-auth-dele
- llm-proxy fields
- triage ClusterRoleBinding triage-controller-skil
- triage Service triage-controller
- skills_test New()
- slacksink Message
- review fields
- coordinator ClusterRoleBinding coordinator-remed
- coordinator NetworkPolicy coordinator
- egress fields
- egress NetworkPolicy egress-gateway
- egress fields
- k8s-mcp fields
- llm-proxy fields
- llm-proxy NetworkPolicy llm-proxy
- signoz-mcp fields
- triage fields
- runs SandboxSpec()
- auth_test TestPodAuthRejections()
- rbac_test AccessFor()
- proxy_test TestBodyLimit()
- k8s-warning-events fields
- coordinator Deployment coordinator
- k8s-mcp Deployment k8s-mcp
- k8s-mcp fields
- signoz-mcp fields
- triage Deployment triage-controller
- skillsource TestCRDSkillsListsValidSkillsAndSkip
- proxy TestProxyOpenAIUpstreamEndToEnd()
- remediation_test Skill
- remediation_types RemediationProposal
- slacksink_test New()
- egress fields
- k8s-mcp ClusterRole triage-k8s-readonly
- llm-proxy Namespace llm-proxy
- llm-proxy fields
- llm-proxy fields
- signoz-mcp fields
- slack-sink fields
- slack-sink fields
- crashloopbackoff fields
- image-pull fields
- oomkilled fields
- pending-pod fields
- probe-failures fields
- service-errors-latency fields
- volume-mount fields
- coordinator ValidatingAdmissionPolicyBinding coo
- llm-proxy Deployment llm-proxy
- signoz-mcp Namespace mcp-gateway
- signoz-mcp Deployment signoz-mcp
- slack-sink Service slack-sink
- slack-sink Deployment slack-sink
- slack-sink fields
- slack-sink fields
- remediate RemediationDecision
- runtimeclass fields
- signoz-mcp fields
- signoz-mcp fields
- slack-sink fields
- slack-sink fields
- k8s-mcp ConfigMap k8s-mcp-config
- signoz-mcp ServiceAccount signoz-mcp
- 0002-custom-resources-as-run-state Report size l
- executor_test Reconciler
- go-kubernetes Kubernetes code and manifest rules
- go-project-layout Project layout rules
- install-foundry fields
- install-gvisor fields
- go-concurrency Go concurrency rules
- go-style Go style rules
- go-testing Go testing rules
- 0000-template ADR template
- go fields

## God Nodes (most connected - your core abstractions)
1. `kc()` - 34 edges
2. `Remediation` - 29 edges
3. `die()` - 28 edges
4. `ok()` - 27 edges
5. `log()` - 26 edges
6. `retry()` - 25 edges
7. `Server` - 22 edges
8. `Reconciler` - 20 edges
9. `newAgent()` - 19 edges
10. `Reconciler` - 19 edges

## Surprising Connections (you probably didn't know these)
- `Approval policy (auto or required)` --conceptually_related_to--> `Remediation Executor`  [INFERRED]
  .claude/plans/remediation-operator.plan.md → docs/architecture.md
- `k8s-infra collectors step` --conceptually_related_to--> `Alert triage flow`  [INFERRED]
  docs/setup.md → README.md
- `SigNoz alert rule step` --implements--> `Alert triage flow`  [INFERRED]
  docs/setup.md → README.md
- `Working agreements` --conceptually_related_to--> `End-to-end check`  [AMBIGUOUS]
  CLAUDE.md → docs/setup.md
- `main()` --calls--> `New()`  [EXTRACTED]
  cmd/slack-sink/main.go → internal/slacksink/slacksink.go

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Run state as custom resources** — docs_adr_0002_custom_resources_as_run_state_incident_cr, docs_adr_0002_custom_resources_as_run_state_triagerun_cr, docs_adr_0002_custom_resources_as_run_state_single_writer_per_field [EXTRACTED 1.00]
- **Remediation step chain** — docs_architecture_coordinator, docs_architecture_remediation_skill, docs_architecture_step_identity, docs_architecture_remediation_agent, docs_architecture_remediation_executor, docs_architecture_approval, docs_architecture_cooldown, docs_architecture_kill_switch [EXTRACTED 1.00]
- **Triage evidence path** — docs_architecture_signoz, docs_architecture_triage_controller, docs_architecture_triage_run, docs_architecture_sandbox, docs_architecture_triage_agent, docs_architecture_signoz_mcp_server, docs_architecture_k8s_readonly_mcp_server, docs_architecture_llm_proxy, docs_architecture_egress_gateway [INFERRED 0.75]
- **Remediation chain** — readme_triage_flow, readme_remediation, readme_remediation_executor, readme_approval, readme_remediation_mode [INFERRED 0.75]
- **Setup verification steps** — docs_setup_coordinator_checks, docs_setup_remediation_checks, docs_setup_end_to_end_check, docs_setup_signoz_alerts, docs_setup_k8s_infra [INFERRED 0.75]
- **Remediation run components** — docs_architecture_remediation_executor, _claude_plans_remediation_operator_plan_approval_policy, _claude_plans_remediation_operator_plan_remediation_cr, _claude_plans_remediation_operator_plan_remediation_skill_crd, _claude_plans_remediation_operator_plan_least_privilege_identity [INFERRED 0.75]
- **Coordinator step guard** — _claude_plans_coordinator_plan_coordinator, _claude_plans_coordinator_plan_step_identity, _claude_plans_coordinator_plan_coordinator_grantable_clusterrole, _claude_plans_coordinator_plan_step_identities_admission_policy, _claude_plans_coordinator_plan_triagerun_suspend_ordering [INFERRED 0.75]

## Communities (150 total, 25 thin omitted)

### Community 0 - "Package Files And Imports"
Cohesion: 0.07
Nodes (9): env(), envInt(), main(), run(), Decision, ProposedAction, HTTPStatus(), TestHTTPStatus() (+1 more)

### Community 1 - "Setup Script And Skill Docs"
Cohesion: 0.11
Nodes (64): docs/README.md index, read_skill tool, Shipped skills (k8s-warning-events entry point), TriageSkill match field, TriageSkill CRD, apply_apiserver_policy(), apply_local_llm_mode(), apply_secret() (+56 more)

### Community 2 - "CRD Types And Deepcopy"
Cohesion: 0.05
Nodes (20): AlertFrom(), RemediationSkill, RemediationSkillSpec, TriageRun, TriageRunPhase, testRun(), TestTriageIdentityIsPinnedToItsRun(), skillSpec() (+12 more)

### Community 3 - "Coordinator And Remediation Design"
Cohesion: 0.06
Nodes (39): Coordinator for step order and per-step identity, coordinator-grantable ClusterRole, Incident and TriageRun custom resources, coordinator-step-identities ValidatingAdmissionPolicy, Per-step ServiceAccount, Role, and RoleBinding, TriageRun suspend-then-resume ordering, Approval policy (auto or required), Per-remediation-run least-privilege identity (+31 more)

### Community 4 - "Incident CRD Schema"
Cohesion: 0.05
Nodes (36): jsonPath, name, type, controller-gen.kubebuilder.io/version, apiVersion, kind, metadata, annotations (+28 more)

### Community 5 - "Remediation CRD Schema"
Cohesion: 0.05
Nodes (36): jsonPath, name, type, controller-gen.kubebuilder.io/version, apiVersion, kind, metadata, annotations (+28 more)

### Community 6 - "TriageRun CRD Schema"
Cohesion: 0.05
Nodes (36): jsonPath, name, type, controller-gen.kubebuilder.io/version, apiVersion, kind, metadata, annotations (+28 more)

### Community 7 - "Coordinator Reconciler"
Cohesion: 0.09
Nodes (9): Incident, IncidentPhase, TriageRun, harness, Reconciler, IdentityName(), RemediationIdentity(), RemediationIdentityName() (+1 more)

### Community 8 - "MCP Toolbox And OpenAI Translation"
Cohesion: 0.10
Nodes (25): fakeToolbox, MCPToolbox, ConnectMCP(), schemaMap(), blocksOf(), fromOpenAI(), reply(), TestFromOpenAIToolCallInText() (+17 more)

### Community 9 - "RemediationSkill CRD Schema"
Cohesion: 0.06
Nodes (34): jsonPath, name, type, controller-gen.kubebuilder.io/version, apiVersion, kind, metadata, annotations (+26 more)

### Community 10 - "Remediation Namespace RBAC"
Cohesion: 0.09
Nodes (33): Role remediation-target-reader, RoleBinding coordinator-remediation-target-reader, Role remediation-executor, RoleBinding triage-controller-remediation-executor, name, namespace, apiVersion, kind (+25 more)

### Community 11 - "Crashloop Demo Workload"
Cohesion: 0.06
Nodes (32): command, env, image, name, resources, securityContext, apiVersion, kind (+24 more)

### Community 12 - "Remediation Executor"
Cohesion: 0.13
Nodes (19): Remediation, RemediationPhase, AgentAuth, Authorizer, Driver, Notifier, Reconciler, Server (+11 more)

### Community 13 - "gVisor Smoke Test"
Cohesion: 0.07
Nodes (29): apiVersion, drop, command, image, name, resources, securityContext, kind (+21 more)

### Community 14 - "TriageSkill CRD Schema"
Cohesion: 0.07
Nodes (28): jsonPath, name, type, apiVersion, kind, metadata, name, kind (+20 more)

### Community 15 - "Egress Network Policies"
Cohesion: 0.09
Nodes (28): NetworkPolicy signoz-mcp-egress, NetworkPolicy k8s-mcp-egress, NetworkPolicy triage-controller-egress, ports, to, app.kubernetes.io/name, name, namespace (+20 more)

### Community 16 - "Triage Server Tests"
Cohesion: 0.13
Nodes (23): TestRemediateStopsAtTheTurnBudget(), testScheme(), TestTokenFileTransportRereadsToken(), TestSandboxObjectIsLockedDown(), fingerprintLabel(), newHarness(), startedRun(), TestAgentErrorFailsRun() (+15 more)

### Community 17 - "SigNoz Foundry Casting"
Cohesion: 0.07
Nodes (27): foundry.signoz.io/kubernetes-namespace, apiVersion, flavor, mode, spec, kind, metadata, annotations (+19 more)

### Community 18 - "Triage Agent API"
Cohesion: 0.13
Nodes (8): writeError(), writeJSONBody(), RunFrom(), NewServer(), truncate(), writeJSON(), Config, Server

### Community 19 - "k8s-infra Collector Values"
Cohesion: 0.09
Nodes (23): enabled, global, clusterName, deploymentEnvironment, enabled, enabled, memory, enabled (+15 more)

### Community 20 - "Agent Authentication"
Cohesion: 0.21
Nodes (8): fakeAuth, Caller, BearerToken(), Verifier, allowLocal, RBACAuthorizer, AgentAuth, fakeAuth

### Community 21 - "Agent Loop"
Cohesion: 0.20
Nodes (10): Agent, Budget, Model, Reply, scriptedModel, Tool, Toolbox, ToolCall (+2 more)

### Community 22 - "README And Working Agreements"
Cohesion: 0.12
Nodes (21): docs/architecture.md, Working agreements, Coordinator setup checks, End-to-end check, k8s-infra collectors step, Remediation RBAC checks, Remediation setup step, SigNoz alert rule step (+13 more)

### Community 23 - "Coordinator Test Harness"
Cohesion: 0.17
Nodes (19): TriageIdentity(), kindOf(), newHarness(), newIncident(), runFor(), TestExpiredIncidentIsDeletedWithItsRun(), TestFinishedRunRevokesIdentity(), TestIncidentWaitsWhenRunSlotsAreFull() (+11 more)

### Community 24 - "Remediation Policy And Targets"
Cohesion: 0.16
Nodes (16): podOwner(), ResolveTarget(), verifyWorkload(), actionFits(), Choose(), SkillFrom(), Owner(), namespaceAllowed() (+8 more)

### Community 25 - "Admission Policy Rules"
Cohesion: 0.10
Nodes (21): ValidatingAdmissionPolicy coordinator-step-identities, expression, name, resourceRules, apiGroups, apiVersions, operations, resources (+13 more)

### Community 26 - "Triage Network Policies"
Cohesion: 0.10
Nodes (21): NetworkPolicy triage-agent, NetworkPolicy mcp-from-agents-only, NetworkPolicy triage-controller-ingress, to, namespaceSelector, podSelector, from, apiVersion (+13 more)

### Community 27 - "Coordinator Manifests"
Cohesion: 0.15
Nodes (19): apiVersion, kind, metadata, rules, apiVersion, kind, metadata, rules (+11 more)

### Community 28 - "Sandbox Launcher"
Cohesion: 0.19
Nodes (8): fakeLauncher, Launcher, envVar(), Spec, Config, Launcher, fakeLauncher, Launcher

### Community 29 - "Agent Tests"
Cohesion: 0.26
Nodes (17): call(), newAgent(), TestTriageEnforcesBudgets(), TestTriageHappyPath(), TestTriageIgnoresInvalidReportText(), TestTriageInvalidReportIsSentBack(), TestTriageNudgesWhenModelStopsWithoutReport(), TestTriageRecoversReportWrittenAsText() (+9 more)

### Community 30 - "Binary Env Config"
Cohesion: 0.13
Nodes (12): env(), envDuration(), envInt(), main(), run(), env(), envDuration(), main() (+4 more)

### Community 31 - "Coordinator Deployment"
Cohesion: 0.11
Nodes (18): env, image, imagePullPolicy, livenessProbe, name, ports, readinessProbe, resources (+10 more)

### Community 32 - "Egress Gateway Config"
Cohesion: 0.14
Nodes (17): apiVersion, data, kind, metadata, envoy.yaml, Namespace egress, ConfigMap egress-gateway, Service egress-gateway (+9 more)

### Community 33 - "Triage Controller Deployment"
Cohesion: 0.11
Nodes (18): env, image, imagePullPolicy, livenessProbe, name, ports, readinessProbe, resources (+10 more)

### Community 34 - "Executor And Remediate Tests"
Cohesion: 0.21
Nodes (16): TestRemediateNoAction(), TestRemediateRetriesARefusedProposal(), call(), newHarness(), newRemediation(), proposed(), serverFor(), TestAgentThatMissesItsDeadlineFails() (+8 more)

### Community 35 - "Triage Agent Binary"
Cohesion: 0.21
Nodes (11): assignmentPrompt(), connectTools(), investigate(), loadConfig(), model(), decide(), remediationPrompt(), Assignment (+3 more)

### Community 36 - "Triage Namespaces"
Cohesion: 0.12
Nodes (15): apiVersion, kind, metadata, Namespace alert-triage, Namespace agent-sandboxes, ServiceAccount triage-controller, ClusterRoleBinding triage-controller-auth-delegator, apiVersion (+7 more)

### Community 37 - "Triage Controller RBAC"
Cohesion: 0.16
Nodes (16): apiVersion, kind, metadata, rules, Role triage-controller, Role triage-controller-intake, ClusterRole triage-skills-reader, apiVersion (+8 more)

### Community 38 - "Lean Collector Values"
Cohesion: 0.15
Nodes (14): collectionInterval, enabled, memory, otelAgent, resources, otelDeployment, resources, presets (+6 more)

### Community 39 - "K8s MCP Identity"
Cohesion: 0.17
Nodes (15): metadata, metadata, metadata, ServiceAccount k8s-mcp, Service k8s-mcp, app.kubernetes.io/name, labels, name (+7 more)

### Community 40 - "Kind Cluster Config"
Cohesion: 0.13
Nodes (14): apiVersion, containerdConfigPatches, containerPath, hostPath, readOnly, kind, name, networking (+6 more)

### Community 41 - "Lean Kind Cluster"
Cohesion: 0.13
Nodes (14): apiVersion, containerdConfigPatches, containerPath, hostPath, readOnly, kind, name, networking (+6 more)

### Community 42 - "Starter RemediationSkill"
Cohesion: 0.13
Nodes (14): constraints, type, apiVersion, maxReplicas, namespaces, kind, alertname, metadata (+6 more)

### Community 43 - "Run Notifications"
Cohesion: 0.21
Nodes (8): Title(), fakeNotifier, LogNotifier, MultiNotifier, Notifier, Run, SlackNotifier, Status

### Community 44 - "Report Parsing"
Cohesion: 0.22
Nodes (10): parseReport(), jsonObjects(), reportInText(), Report, Schema(), TestMarkdownIncludesKeyParts(), TestValidate(), valid() (+2 more)

### Community 45 - "skills Task"
Cohesion: 0.26
Nodes (4): Task, Library, Skill, TestPromptSection()

### Community 46 - "installation fields"
Cohesion: 0.14
Nodes (13): apiVersion, ipPools, blockSize, cidr, encapsulation, name, natOutgoing, nodeSelector (+5 more)

### Community 47 - "coordinator ServiceAccount coordinator"
Cohesion: 0.18
Nodes (14): metadata, ServiceAccount coordinator, RoleBinding coordinator-grantable, app.kubernetes.io/name, labels, name, namespace, metadata (+6 more)

### Community 48 - "triage fields"
Cohesion: 0.25
Nodes (14): metadata, app.kubernetes.io/name, labels, name, namespace, metadata, metadata, metadata (+6 more)

### Community 49 - "actions Kube"
Cohesion: 0.22
Nodes (8): Kube, fakeDriver, TestDriverRefusesMismatchedKinds(), TestOpenPRChangesNothing(), TestRestartPodDeletesIt(), TestRolloutRestartAnnotatesTheTemplate(), Proposal, Params

### Community 50 - "main TokenFileTransport"
Cohesion: 0.26
Nodes (6): main(), run(), remediate(), TokenFileTransport, Submission, controller

### Community 51 - "triage RoleBinding triage-controller-intake"
Cohesion: 0.23
Nodes (13): subjects, subjects, RoleBinding triage-controller-intake, RoleBinding triage-controller, apiVersion, apiVersion, kind, subjects (+5 more)

### Community 52 - "? TestCheckAllowsPodBoundServiceAccount()"
Cohesion: 0.21
Nodes (6): fakeAccess, newVerifier(), podBoundUser(), request(), TestCheckAllowsPodBoundServiceAccount(), TestCheckRejections()

### Community 53 - "claude Claude"
Cohesion: 0.24
Nodes (4): Claude, NewClaude(), toolParam(), toStrings()

### Community 54 - "alert Webhook"
Cohesion: 0.21
Nodes (7): Webhook, Alert, Parse(), TestKeyWithoutFingerprint(), TestParseAndFiring(), TestParseRejectsGarbage(), copyFlush()

### Community 55 - "egress Deployment egress-gateway"
Cohesion: 0.18
Nodes (12): triage.agent-sandbox/config-version, apiVersion, kind, metadata, spec, Deployment egress-gateway, app.kubernetes.io/name, annotations (+4 more)

### Community 56 - "k8s-mcp ClusterRoleBinding triage-k8s-readonly"
Cohesion: 0.17
Nodes (12): apiVersion, kind, metadata, roleRef, subjects, ClusterRoleBinding triage-k8s-readonly, apiGroup, kind (+4 more)

### Community 57 - "k8s-mcp fields"
Cohesion: 0.17
Nodes (12): type, runAsGroup, runAsNonRoot, runAsUser, seccompProfile, securityContext, serviceAccountName, volumes (+4 more)

### Community 58 - "llm-proxy Service llm-proxy"
Cohesion: 0.21
Nodes (12): metadata, metadata, Service llm-proxy, app.kubernetes.io/component, app.kubernetes.io/name, labels, name, namespace (+4 more)

### Community 59 - "? Identity"
Cohesion: 0.20
Nodes (4): Identity, stepIdentity(), TestAllowedRejectsBroadIdentities(), TestRemediationRulesAreAllowed()

### Community 60 - "coordinator RoleBinding coordinator"
Cohesion: 0.31
Nodes (11): roleRef, RoleBinding coordinator, RoleBinding coordinator, apiVersion, roleRef, kind, metadata, roleRef (+3 more)

### Community 61 - "egress fields"
Cohesion: 0.18
Nodes (11): type, runAsGroup, runAsNonRoot, runAsUser, seccompProfile, automountServiceAccountToken, securityContext, volumes (+3 more)

### Community 62 - "llm-proxy ClusterRoleBinding llm-proxy-auth-dele"
Cohesion: 0.18
Nodes (11): apiVersion, kind, roleRef, subjects, ClusterRoleBinding llm-proxy-auth-delegator, apiGroup, kind, name (+3 more)

### Community 63 - "llm-proxy fields"
Cohesion: 0.18
Nodes (11): app.kubernetes.io/component, app.kubernetes.io/name, matchLabels, name, targetPort, app.kubernetes.io/name, matchLabels, spec (+3 more)

### Community 64 - "triage ClusterRoleBinding triage-controller-skil"
Cohesion: 0.29
Nodes (11): roleRef, apiVersion, kind, metadata, roleRef, ClusterRoleBinding triage-controller-skills, roleRef, roleRef (+3 more)

### Community 65 - "triage Service triage-controller"
Cohesion: 0.20
Nodes (11): Service triage-controller, ports, ports, name, port, protocol, targetPort, apiVersion (+3 more)

### Community 66 - "skills_test New()"
Cohesion: 0.25
Nodes (7): New(), skill(), TestMatches(), TestNewRejectsDuplicates(), TestRepoSkills(), TestValidate(), triageSkill

### Community 67 - "slacksink Message"
Cohesion: 0.31
Nodes (3): upstream, Message, Sink

### Community 68 - "review fields"
Cohesion: 0.31
Nodes (5): reviewState, containsAny(), newReviewState(), reviewFeedback(), uninvestigatedSteps()

### Community 69 - "coordinator ClusterRoleBinding coordinator-remed"
Cohesion: 0.29
Nodes (10): apiVersion, kind, metadata, subjects, ClusterRoleBinding coordinator-remediation-skills, subjects, subjects, kind (+2 more)

### Community 70 - "coordinator NetworkPolicy coordinator"
Cohesion: 0.20
Nodes (10): NetworkPolicy coordinator, app.kubernetes.io/name, apiVersion, kind, spec, matchLabels, matchLabels, podSelector (+2 more)

### Community 71 - "egress fields"
Cohesion: 0.20
Nodes (10): args, image, livenessProbe, name, ports, readinessProbe, resources, securityContext (+2 more)

### Community 72 - "egress NetworkPolicy egress-gateway"
Cohesion: 0.20
Nodes (10): NetworkPolicy egress-gateway, to, apiVersion, kind, spec, egress, policyTypes, ipBlock (+2 more)

### Community 73 - "egress fields"
Cohesion: 0.20
Nodes (10): app.kubernetes.io/name, matchLabels, name, targetPort, app.kubernetes.io/name, matchLabels, spec, podSelector (+2 more)

### Community 74 - "k8s-mcp fields"
Cohesion: 0.20
Nodes (10): args, image, livenessProbe, name, ports, readinessProbe, resources, securityContext (+2 more)

### Community 75 - "llm-proxy fields"
Cohesion: 0.20
Nodes (10): env, image, imagePullPolicy, livenessProbe, name, ports, readinessProbe, resources (+2 more)

### Community 76 - "llm-proxy NetworkPolicy llm-proxy"
Cohesion: 0.20
Nodes (10): NetworkPolicy llm-proxy, namespaceSelector, podSelector, from, apiVersion, kind, metadata, spec (+2 more)

### Community 77 - "signoz-mcp fields"
Cohesion: 0.20
Nodes (10): env, image, livenessProbe, name, ports, readinessProbe, resources, securityContext (+2 more)

### Community 78 - "triage fields"
Cohesion: 0.20
Nodes (10): key, operator, values, app.kubernetes.io/name, matchExpressions, matchLabels, app.kubernetes.io/name, matchLabels (+2 more)

### Community 79 - "runs SandboxSpec()"
Cohesion: 0.42
Nodes (3): finishStatus(), SandboxSpec(), RunReconciler

### Community 80 - "auth_test TestPodAuthRejections()"
Cohesion: 0.29
Nodes (7): bearer(), pod(), podAuth(), podAuthAs(), TestPodAuthRejections(), TestPodAuthResolvesRunFromPodLabel(), PodAuth

### Community 81 - "rbac_test AccessFor()"
Cohesion: 0.27
Nodes (9): AccessFor(), resourceFor(), ResourceName(), RunRules(), ruleAllows(), TestAccessForMatchesRunRules(), TestRunRulesGrantNothingOutsideScope(), TestRunRulesOnlyActionsValidInTargetNamespace() (+1 more)

### Community 82 - "proxy_test TestBodyLimit()"
Cohesion: 0.47
Nodes (9): newProxy(), post(), TestBodyLimit(), TestCountTokensSkipsMaxTokens(), TestForwardsAllowedRequestWithServerKey(), TestOnlyMessagesEndpoints(), TestPolicyRejections(), TestRBACDecidesCallerAndModel() (+1 more)

### Community 83 - "k8s-warning-events fields"
Cohesion: 0.20
Nodes (9): apiVersion, kind, alertname, metadata, name, spec, description, instructions (+1 more)

### Community 84 - "coordinator Deployment coordinator"
Cohesion: 0.22
Nodes (9): apiVersion, kind, spec, Deployment coordinator, replicas, strategy, template, type (+1 more)

### Community 85 - "k8s-mcp Deployment k8s-mcp"
Cohesion: 0.22
Nodes (9): triage.agent-sandbox/config-version, apiVersion, kind, spec, Deployment k8s-mcp, annotations, replicas, template (+1 more)

### Community 86 - "k8s-mcp fields"
Cohesion: 0.22
Nodes (9): app.kubernetes.io/name, name, port, targetPort, app.kubernetes.io/name, matchLabels, spec, ports (+1 more)

### Community 87 - "signoz-mcp fields"
Cohesion: 0.22
Nodes (9): app.kubernetes.io/name, name, port, targetPort, app.kubernetes.io/name, matchLabels, spec, ports (+1 more)

### Community 88 - "triage Deployment triage-controller"
Cohesion: 0.22
Nodes (9): apiVersion, kind, spec, Deployment triage-controller, replicas, strategy, template, type (+1 more)

### Community 89 - "skillsource TestCRDSkillsListsValidSkillsAndSkip"
Cohesion: 0.25
Nodes (5): skillFromObject(), TestCRDSkillsListsValidSkillsAndSkipsInvalid(), triageSkillObject(), CRDSkills, SkillSource

### Community 90 - "proxy TestProxyOpenAIUpstreamEndToEnd()"
Cohesion: 0.36
Nodes (5): TestProxyOpenAIUpstreamEndToEnd(), New(), Authorizer, Policy, Proxy

### Community 91 - "remediation_test Skill"
Cohesion: 0.44
Nodes (7): crashloopSkill(), ptr(), TestApprovalDefaultsAndOverride(), TestCheckProposal(), TestNamespaceOutsideSkillConstraints(), TestProtectedNamespacesAlwaysDenied(), TestSkillValidate()

### Community 92 - "remediation_types RemediationProposal"
Cohesion: 0.32
Nodes (6): RemediationProposal, RemediationTarget, ProposalFrom(), TargetFrom(), RemediationAction, RemediationStatus

### Community 93 - "slacksink_test New()"
Cohesion: 0.32
Nodes (6): main(), New(), post(), TestNewestFirstAndCapped(), TestPageEscapesReportText(), TestReceiveLikeSlack()

### Community 94 - "egress fields"
Cohesion: 0.29
Nodes (8): ports, namespaceSelector, podSelector, from, ports, port, protocol, ingress

### Community 95 - "k8s-mcp ClusterRole triage-k8s-readonly"
Cohesion: 0.25
Nodes (7): apiVersion, kind, rules, ClusterRole triage-k8s-readonly, apiGroups, resources, verbs

### Community 96 - "llm-proxy Namespace llm-proxy"
Cohesion: 0.25
Nodes (7): Namespace llm-proxy, ServiceAccount llm-proxy, apiVersion, kind, metadata, apiVersion, kind

### Community 97 - "llm-proxy fields"
Cohesion: 0.29
Nodes (8): ports, to, ports, port, protocol, egress, namespaceSelector, podSelector

### Community 98 - "llm-proxy fields"
Cohesion: 0.25
Nodes (8): type, runAsGroup, runAsNonRoot, runAsUser, seccompProfile, securityContext, serviceAccountName, spec

### Community 99 - "signoz-mcp fields"
Cohesion: 0.36
Nodes (8): metadata, app.kubernetes.io/name, labels, name, namespace, metadata, metadata, metadata

### Community 100 - "slack-sink fields"
Cohesion: 0.25
Nodes (8): image, imagePullPolicy, name, ports, readinessProbe, resources, securityContext, containers

### Community 101 - "slack-sink fields"
Cohesion: 0.25
Nodes (8): type, runAsGroup, runAsNonRoot, runAsUser, seccompProfile, automountServiceAccountToken, securityContext, spec

### Community 102 - "crashloopbackoff fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 103 - "image-pull fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 104 - "oomkilled fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 105 - "pending-pod fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 106 - "probe-failures fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 107 - "service-errors-latency fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 108 - "volume-mount fields"
Cohesion: 0.25
Nodes (7): apiVersion, kind, metadata, name, spec, description, instructions

### Community 109 - "coordinator ValidatingAdmissionPolicyBinding coo"
Cohesion: 0.29
Nodes (7): ValidatingAdmissionPolicyBinding coordinator-step-identities, policyName, validationActions, apiVersion, kind, metadata, spec

### Community 110 - "llm-proxy Deployment llm-proxy"
Cohesion: 0.29
Nodes (7): apiVersion, kind, spec, Deployment llm-proxy, replicas, template, metadata

### Community 111 - "signoz-mcp Namespace mcp-gateway"
Cohesion: 0.29
Nodes (6): Namespace mcp-gateway, Service signoz-mcp, apiVersion, kind, apiVersion, kind

### Community 112 - "signoz-mcp Deployment signoz-mcp"
Cohesion: 0.29
Nodes (7): apiVersion, kind, spec, Deployment signoz-mcp, replicas, template, metadata

### Community 113 - "slack-sink Service slack-sink"
Cohesion: 0.29
Nodes (6): Service slack-sink, NetworkPolicy slack-sink, apiVersion, kind, apiVersion, kind

### Community 114 - "slack-sink Deployment slack-sink"
Cohesion: 0.29
Nodes (7): apiVersion, kind, spec, Deployment slack-sink, replicas, template, metadata

### Community 115 - "slack-sink fields"
Cohesion: 0.43
Nodes (7): metadata, app.kubernetes.io/name, labels, name, namespace, metadata, metadata

### Community 116 - "slack-sink fields"
Cohesion: 0.29
Nodes (7): app.kubernetes.io/name, matchLabels, app.kubernetes.io/name, matchLabels, spec, podSelector, selector

### Community 117 - "remediate RemediationDecision"
Cohesion: 0.40
Nodes (4): RemediationDecision, RemediationTask, Submitter, parseDecision()

### Community 118 - "runtimeclass fields"
Cohesion: 0.33
Nodes (5): apiVersion, handler, kind, metadata, name

### Community 119 - "signoz-mcp fields"
Cohesion: 0.33
Nodes (6): type, runAsGroup, runAsNonRoot, runAsUser, seccompProfile, securityContext

### Community 120 - "signoz-mcp fields"
Cohesion: 0.33
Nodes (6): automountServiceAccountToken, serviceAccountName, volumes, spec, emptyDir, name

### Community 121 - "slack-sink fields"
Cohesion: 0.33
Nodes (6): podSelector, from, spec, egress, ingress, policyTypes

### Community 122 - "slack-sink fields"
Cohesion: 0.33
Nodes (6): ports, name, port, protocol, targetPort, ports

### Community 123 - "k8s-mcp ConfigMap k8s-mcp-config"
Cohesion: 0.40
Nodes (5): apiVersion, data, kind, config.toml, ConfigMap k8s-mcp-config

### Community 124 - "signoz-mcp ServiceAccount signoz-mcp"
Cohesion: 0.50
Nodes (4): ServiceAccount signoz-mcp, apiVersion, automountServiceAccountToken, kind

## Ambiguous Edges - Review These
- `End-to-end check` → `Working agreements`  [AMBIGUOUS]
  CLAUDE.md · relation: conceptually_related_to

## Knowledge Gaps
- **29 isolated node(s):** `k8s-agent-sandbox`, `Kubernetes code and manifest rules`, `Project layout rules`, `Go concurrency rules`, `Go style rules` (+24 more)
  These have ≤1 connection - possible missing edges or undocumented components. (Counts symbols only; 705 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **25 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Remediation` connect `Remediation Executor` to `Package Files And Imports`, `CRD Types And Deepcopy`, `Executor And Remediate Tests`, `Coordinator Reconciler`, `Coordinator Test Harness`, `remediation_types RemediationProposal`, `executor_test Reconciler`?**
  _High betweenness centrality (0.005) - this node is a cross-community bridge._
- **What connects `k8s-agent-sandbox`, `Kubernetes code and manifest rules`, `Project layout rules` to the rest of the system?**
  _29 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Package Files And Imports` be split into smaller, more focused modules?**
  _Cohesion score 0.07040328092959672 - nodes in this community are weakly interconnected._
- **Why does `newVerifier()` connect `? TestCheckAllowsPodBoundServiceAccount()` to `Package Files And Imports`, `Agent Authentication`?**
  _High betweenness centrality (0.004) - this node is a cross-community bridge._
- **Should `Setup Script And Skill Docs` be split into smaller, more focused modules?**
  _Cohesion score 0.114880144730891 - nodes in this community are weakly interconnected._
- **Why does `Alert` connect `CRD Types And Deepcopy` to `Package Files And Imports`?**
  _High betweenness centrality (0.004) - this node is a cross-community bridge._

### Low-confidence Hints
_AMBIGUOUS edges — the extractor was unsure. Verify before acting on these._

- **What is the exact relationship between `End-to-end check` and `Working agreements`?**
  _Edge tagged AMBIGUOUS (relation: conceptually_related_to) - confidence is low._