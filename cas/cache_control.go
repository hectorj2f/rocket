package cas

import (
	"strconv"
	"strings"
	"time"
)

type CacheControl struct {
	// MaxAge<0 means don't use cached image, equivalently 'Max-Age: 0'
	// MaxAge>0 means Max-Age attribute present and given in seconds
	// MaxAge=0 means no 'Max-Age' attribute specified.
	MaxAge    int
	NoStore   bool
	NoCache   bool
	Freshness time.Time
}

func NewCache(headerValue string) *CacheControl {
	cc := &CacheControl{}

	cc.Freshness = time.Now()
	cc.NoStore = false
	cc.NoCache = false
	cc.MaxAge = -1

	if len(headerValue) > 0 {
		parts := strings.Split(headerValue, " ")
		for i := 0; i < len(parts); i++ {
			attr, val := parts[i], ""
			if j := strings.Index(attr, "="); j >= 0 {
				attr, val = attr[:j], attr[j+1:]
			}
			lowerAttr := strings.ToLower(attr)

			switch lowerAttr {
			case "no-store":
				cc.NoStore = true
				continue
			case "no-cache":
				cc.NoCache = true
				continue
			case "max-age":
				secs, err := strconv.Atoi(val)
				if err != nil || secs != 0 && val[0] == '0' {
					break
				}
				if secs <= 0 {
					cc.MaxAge = -1
					cc.NoCache = true
				} else {
					cc.MaxAge = secs
				}
				continue
			}
		}
	}
	return cc
}

func (cc CacheControl) UseCachedImage() bool {
	// Return True if a valid image exists in the cache, otherwise
	// return False.
	freshnessLifetime := int(time.Now().Sub(cc.Freshness).Seconds())
	if (cc.MaxAge > -1 && freshnessLifetime < cc.MaxAge) && !cc.NoStore && !cc.NoCache {
		return true
	}

	return false
}
