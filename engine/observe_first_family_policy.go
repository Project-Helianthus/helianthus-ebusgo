package engine

type ObserveFirstFamily string

const (
	ObserveFirstFamilyOther ObserveFirstFamily = "other"
	ObserveFirstFamilyB509  ObserveFirstFamily = "B509"
	ObserveFirstFamilyB516  ObserveFirstFamily = "B516"
	ObserveFirstFamilyB524  ObserveFirstFamily = "B524"
	ObserveFirstFamilyB555  ObserveFirstFamily = "B555"
)

type ObserveFirstTrafficScope string

const (
	ObserveFirstTrafficScopeActive  ObserveFirstTrafficScope = "active"
	ObserveFirstTrafficScopePassive ObserveFirstTrafficScope = "passive"
)

type ObserveFirstRequestIntent string

const (
	ObserveFirstRequestIntentUnknown   ObserveFirstRequestIntent = "unknown"
	ObserveFirstRequestIntentRead      ObserveFirstRequestIntent = "read"
	ObserveFirstRequestIntentWrite     ObserveFirstRequestIntent = "write"
	ObserveFirstRequestIntentBroadcast ObserveFirstRequestIntent = "broadcast"
)

type ObserveFirstDirectApplyPolicy string

const (
	ObserveFirstDirectApplyPolicyNever           ObserveFirstDirectApplyPolicy = "never"
	ObserveFirstDirectApplyPolicyStateDefault    ObserveFirstDirectApplyPolicy = "state_default"
	ObserveFirstDirectApplyPolicyConfigOptIn     ObserveFirstDirectApplyPolicy = "config_opt_in"
	ObserveFirstDirectApplyPolicyEnergyMergeOnly ObserveFirstDirectApplyPolicy = "energy_merge_only"
)

type ObserveFirstFamilyPolicy struct {
	Family                         ObserveFirstFamily
	RequestIntent                  ObserveFirstRequestIntent
	ResponseClass                  DedupResponseClass
	CorrelationPolicy              WatchCorrelationPolicy
	DirectApplyPolicy              ObserveFirstDirectApplyPolicy
	UsesRuntimeExternalWritePolicy bool
	EffectiveExternalWritePolicy   ObserveFirstExternalWritePolicy
}
