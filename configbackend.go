package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/fatih/structs"
	"github.com/nmcclain/ldap"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type configHandler struct {
	cfg *config
}

type ServiceAuth struct {
	Ret    bool   `json:"ret"`
	Ticket string `json:"ticket"`
	Error  string `json:"error"`
}

func newConfigHandler(cfg *config) Backend {
	handler := configHandler{
		cfg: cfg,
	}
	return handler
}

//
func (h configHandler) Bind(bindDN, bindSimplePw string, conn net.Conn) (resultCode ldap.LDAPResultCode, err error) {
	bindDN = strings.ToLower(bindDN)
	baseDN := strings.ToLower("," + h.cfg.Backend.BaseDN)
	log.Debug("Bind request as %s from %s", bindDN, conn.RemoteAddr().String())
	stats_frontend.Add("bind_reqs", 1)

	// parse the bindDN
	if !strings.HasSuffix(bindDN, baseDN) {
		log.Warning(fmt.Sprintf("Bind Error: BindDN %s not our BaseDN %s", bindDN, h.cfg.Backend.BaseDN))
		return ldap.LDAPResultInvalidCredentials, nil
	}
	parts := strings.Split(strings.TrimSuffix(bindDN, baseDN), ",")
	groupName := ""
	userName := ""
	if len(parts) == 1 {
		userName = strings.TrimPrefix(parts[0], h.cfg.Backend.NameAttr+"=")
	} else if len(parts) == 2 {
		userName = strings.TrimPrefix(parts[0], h.cfg.Backend.NameAttr+"=")
		groupName = strings.TrimPrefix(parts[1], "ou=")
	} else {
		log.Warning(fmt.Sprintf("Bind Error: BindDN %s should have only one or two parts (has %d)", bindDN, len(parts)))
		return ldap.LDAPResultInvalidCredentials, nil
	}
	// find the user
	user := configUser{}
	found := false
	for _, u := range h.cfg.Users {
		if u.Name == userName {
			found = true
			user = u
		}
	}
	if !found {
		log.Warning(fmt.Sprintf("Bind Error: User %s not found.", userName))
		return ldap.LDAPResultInvalidCredentials, nil
	}
	// find the group
	group := configGroup{}
	found = false
	for _, g := range h.cfg.Groups {
		if g.Name == groupName {
			found = true
			group = g
		}
	}
	if !found {
		log.Warning(fmt.Sprintf("Bind Error: Group %s not found.", groupName))
		return ldap.LDAPResultInvalidCredentials, nil
	}
	// validate group membership
	if user.PrimaryGroup != group.UnixID {
		log.Warning(fmt.Sprintf("Bind Error: User %s primary group is not %s.", userName, groupName))
		return ldap.LDAPResultInvalidCredentials, nil
	}

	// finally, validate user's pw
	geturl, err := url.Parse(h.cfg.Backend.AuthURL)
	if err != nil {
		log.Error(fmt.Sprintf("error:%s", err))
	}
	s := structs.New(user)
	mailfield := s.Field("Mail")
	mail := mailfield.Value().(string)
	log.Debug(fmt.Sprintf("mail:%s", mail))
	//pwfield := s.Field("bindSimplePw")
	//pw := pwfield.Value().(string)
	//log.Debug(fmt.Sprintf("pw:%s", pw))
	v := url.Values{}
	v.Set("service", "login")
	v.Set("username", mail)
	v.Set("password", bindSimplePw)
	v.Set("token", "")
	geturl.RawQuery = v.Encode()
	log.Debug(geturl.String())
	resp, err := http.Get(geturl.String())
	if err != nil {
		log.Error("cas http get failed: %s", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("cas http read failed: %s", err)
	}
	log.Debug(fmt.Sprintf("body:%s", body))
	var ret ServiceAuth
	err = json.Unmarshal(body, &ret)
	if err != nil {
		log.Error("cas http json failed: %s", err)
	}
	log.Debug(fmt.Sprintf("ret: %+v", ret))
	if ret.Ret == true {
		stats_frontend.Add("bind_successes", 1)
		log.Debug("Bind success (CAS) as %s from  %s", bindDN, conn.RemoteAddr().String())
		return ldap.LDAPResultSuccess, nil
	}
	// try the old way.
	hash := sha256.New()
	hash.Write([]byte(bindSimplePw))
	if user.PassSHA256 != hex.EncodeToString(hash.Sum(nil)) {
		log.Warning(fmt.Sprintf("Bind Error: invalid credentials as %s from %s", bindDN, conn.RemoteAddr().String()))
		return ldap.LDAPResultInvalidCredentials, nil
	}
	stats_frontend.Add("bind_successes", 1)
	log.Debug("Bind success (LOCAL) as %s from %s", bindDN, conn.RemoteAddr().String())
	return ldap.LDAPResultSuccess, nil
}

//
func (h configHandler) Search(bindDN string, searchReq ldap.SearchRequest, conn net.Conn) (result ldap.ServerSearchResult, err error) {
	bindDN = strings.ToLower(bindDN)
	baseDN := strings.ToLower("," + h.cfg.Backend.BaseDN)
	searchBaseDN := strings.ToLower(searchReq.BaseDN)
	log.Debug("Search request as %s from %s for %s", bindDN, conn.RemoteAddr().String(), searchReq.Filter)
	stats_frontend.Add("search_reqs", 1)

	// validate the user is authenticated and has appropriate access
	if len(bindDN) < 1 {
		return ldap.ServerSearchResult{ResultCode: ldap.LDAPResultInsufficientAccessRights}, fmt.Errorf("Search Error: Anonymous BindDN not allowed %s", bindDN)
	}
	if !strings.HasSuffix(bindDN, baseDN) {
		return ldap.ServerSearchResult{ResultCode: ldap.LDAPResultInsufficientAccessRights}, fmt.Errorf("Search Error: BindDN %s not in our BaseDN %s", bindDN, h.cfg.Backend.BaseDN)
	}
	if !strings.HasSuffix(searchBaseDN, h.cfg.Backend.BaseDN) {
		return ldap.ServerSearchResult{ResultCode: ldap.LDAPResultInsufficientAccessRights}, fmt.Errorf("Search Error: search BaseDN %s is not in our BaseDN %s", searchBaseDN, h.cfg.Backend.BaseDN)
	}
	// return all users in the config file - the LDAP library will filter results for us
	entries := []*ldap.Entry{}
	filterEntity, err := ldap.GetFilterObjectClass(searchReq.Filter)
	if err != nil {
		return ldap.ServerSearchResult{ResultCode: ldap.LDAPResultOperationsError}, fmt.Errorf("Search Error: error parsing filter: %s", searchReq.Filter)
	}
	switch filterEntity {
	default:
		return ldap.ServerSearchResult{ResultCode: ldap.LDAPResultOperationsError}, fmt.Errorf("Search Error: unhandled filter type: %s [%s]", filterEntity, searchReq.Filter)
	case "top":
		h.searchOrganizationalUnits(entries)
		h.searchGroups(entries)
		h.searchUsers(entries)
	case "organizationalunit":
		h.searchOrganizationalUnits(entries)
	case "posixgroup":
		h.searchGroups(entries)
	case "posixaccount", "":
		h.searchUsers(entries)
	}
	stats_frontend.Add("search_successes", 1)
	log.Debug("AP: Search OK: %s", searchReq.Filter)
	return ldap.ServerSearchResult{entries, []string{}, []ldap.Control{}, ldap.LDAPResultSuccess}, nil
}

//
func (h configHandler) searchOrganizationalUnits(entries []*ldap.Entry) {
	ous := []string{"Services", "People", h.cfg.Backend.GroupOU}
	for _, o := range ous {
		attrs := []*ldap.EntryAttribute{}
		attrs = append(attrs, &ldap.EntryAttribute{"ou", []string{o}})
		attrs = append(attrs, &ldap.EntryAttribute{"objectClass", []string{"top", "organizationalUnit"}})
		dn := fmt.Sprintf("ou=%s,%s", o, h.cfg.Backend.BaseDN)
		entries = append(entries, &ldap.Entry{dn, attrs})
	}
}

//
func (h configHandler) searchGroups(entries []*ldap.Entry) {
	for _, g := range h.cfg.Groups {
		attrs := []*ldap.EntryAttribute{}
		attrs = append(attrs, &ldap.EntryAttribute{h.cfg.Backend.NameAttr, []string{g.Name}})
		attrs = append(attrs, &ldap.EntryAttribute{"description", []string{fmt.Sprintf("%s via LDAP", g.Name)}})
		attrs = append(attrs, &ldap.EntryAttribute{"gidNumber", []string{fmt.Sprintf("%d", g.UnixID)}})
		attrs = append(attrs, &ldap.EntryAttribute{"objectClass", []string{"posixGroup"}})
		attrs = append(attrs, &ldap.EntryAttribute{"uniqueMember", h.getGroupMembers(g.UnixID)})
		attrs = append(attrs, &ldap.EntryAttribute{"memberUid", h.getGroupMemberIDs(g.UnixID)})
		dn := fmt.Sprintf("%s=%s,ou=%s,%s", h.cfg.Backend.NameAttr, g.Name, h.cfg.Backend.GroupOU, h.cfg.Backend.BaseDN)
		entries = append(entries, &ldap.Entry{dn, attrs})
	}
}

//
func (h configHandler) searchUsers(entries []*ldap.Entry) {
	for _, u := range h.cfg.Users {
		attrs := []*ldap.EntryAttribute{}
		attrs = append(attrs, &ldap.EntryAttribute{h.cfg.Backend.NameAttr, []string{u.Name}})
		attrs = append(attrs, &ldap.EntryAttribute{"uid", []string{u.Name}})
		if u.Mail != "" {
			attrs = append(attrs, &ldap.EntryAttribute{"mail", []string{u.Mail}})
		}
		if u.DisplayName != "" {
			attrs = append(attrs, &ldap.EntryAttribute{"displayName", []string{u.DisplayName}})
		}
		attrs = append(attrs, &ldap.EntryAttribute{"ou", []string{h.getGroupName(u.PrimaryGroup)}})
		attrs = append(attrs, &ldap.EntryAttribute{"uidNumber", []string{fmt.Sprintf("%d", u.UnixID)}})
		attrs = append(attrs, &ldap.EntryAttribute{"accountStatus", []string{"active"}})
		attrs = append(attrs, &ldap.EntryAttribute{"objectClass", []string{"account", "posixAccount", "top", "shadowAccount"}})
		if u.HomeDirectory != "" {
			attrs = append(attrs, &ldap.EntryAttribute{"homeDirectory", []string{u.HomeDirectory}})
		} else {
			attrs = append(attrs, &ldap.EntryAttribute{"homeDirectory", []string{h.cfg.Backend.Home + u.Name}})
		}
		if u.LoginShell != "" {
			attrs = append(attrs, &ldap.EntryAttribute{"loginShell", []string{u.LoginShell}})
		} else {
			attrs = append(attrs, &ldap.EntryAttribute{"loginShell", []string{"/bin/bash"}})
		}
		attrs = append(attrs, &ldap.EntryAttribute{"description", []string{fmt.Sprintf("%s via LDAP", u.Name)}})
		attrs = append(attrs, &ldap.EntryAttribute{"gecos", []string{fmt.Sprintf("%s via LDAP", u.Name)}})
		attrs = append(attrs, &ldap.EntryAttribute{"gidNumber", []string{fmt.Sprintf("%d", u.PrimaryGroup)}})
		attrs = append(attrs, &ldap.EntryAttribute{"memberOf", h.getGroupDNs(u.OtherGroups)})
		if len(u.SSHKeys) > 0 {
			attrs = append(attrs, &ldap.EntryAttribute{"sshPublicKey", u.SSHKeys})
		}
		dn := fmt.Sprintf("%s=%s,ou=%s,%s", h.cfg.Backend.NameAttr, u.Name, h.getGroupName(u.PrimaryGroup), h.cfg.Backend.BaseDN)
		entries = append(entries, &ldap.Entry{dn, attrs})
	}
}

//
func (h configHandler) Close(boundDn string, conn net.Conn) error {
	stats_frontend.Add("closes", 1)
	return nil
}

//
func (h configHandler) getGroupMembers(gid int) []string {
	members := make(map[string]bool)
	for _, u := range h.cfg.Users {
		if u.PrimaryGroup == gid {
			dn := fmt.Sprintf("%s=%s,ou=%s,%s", h.cfg.Backend.NameAttr, u.Name, h.getGroupName(u.PrimaryGroup), h.cfg.Backend.BaseDN)
			members[dn] = true
		} else {
			for _, othergid := range u.OtherGroups {
				if othergid == gid {
					dn := fmt.Sprintf("%s=%s,ou=%s,%s", h.cfg.Backend.NameAttr, u.Name, h.getGroupName(u.PrimaryGroup), h.cfg.Backend.BaseDN)
					members[dn] = true
				}
			}
		}
	}
	m := []string{}
	for k, _ := range members {
		m = append(m, k)
	}
	return m
}

//
func (h configHandler) getGroupMemberIDs(gid int) []string {
	members := make(map[string]bool)
	for _, u := range h.cfg.Users {
		if u.PrimaryGroup == gid {
			members[u.Name] = true
		} else {
			for _, othergid := range u.OtherGroups {
				if othergid == gid {
					members[u.Name] = true
				}
			}
		}
	}
	m := []string{}
	for k, _ := range members {
		m = append(m, k)
	}
	return m
}

//
func (h configHandler) getGroupDNs(gids []int) []string {
	groups := make(map[string]bool)
	for _, gid := range gids {
		for _, g := range h.cfg.Groups {
			if g.UnixID == gid {
				dn := fmt.Sprintf("%s=%s,ou=%s,%s", h.cfg.Backend.NameAttr, g.Name, h.cfg.Backend.GroupOU, h.cfg.Backend.BaseDN)
				groups[dn] = true
			}
		}
	}
	g := []string{}
	for k, _ := range groups {
		g = append(g, k)
	}
	return g
}

//
func (h configHandler) getGroupName(gid int) string {
	for _, g := range h.cfg.Groups {
		if g.UnixID == gid {
			return g.Name
		}
	}
	return ""
}
