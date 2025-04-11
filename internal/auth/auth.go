package auth

type Authorizer struct {
	allowedIDs map[int64]bool
	adminsIDs  map[int64]bool
}

func NewAuthorizer(ids []int64, admins []int64) *Authorizer {
	allowed := make(map[int64]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	adminMap := make(map[int64]bool, len(admins))
	for _, id := range admins {
		adminMap[id] = true
	}
	return &Authorizer{allowedIDs: allowed, adminsIDs: adminMap}
}

func (a *Authorizer) IsAuthorized(userID int64) bool {
	_, ok := a.allowedIDs[userID]
	return ok
}

func (a *Authorizer) IsAdmin(userID int64) bool {
	_, ok := a.adminsIDs[userID]
	return ok
}

func (a *Authorizer) IsAllowed(userID int64) bool {
	return a.IsAuthorized(userID) || a.IsAdmin(userID)
}
