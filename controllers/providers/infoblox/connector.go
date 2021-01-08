package infoblox

import ibclient "github.com/infobloxopen/infoblox-go-client"

type fakeInfobloxConnector struct {
	getObjectObj  interface{}
	getObjectRef  string
	resultObject  interface{}
	fakeRefReturn string
}

func (c *fakeInfobloxConnector) CreateObject(ibclient.IBObject) (string, error) {
	return c.fakeRefReturn, nil
}

func (c *fakeInfobloxConnector) GetObject(ibclient.IBObject, string, interface{}) (err error) {
	return nil
}

func (c *fakeInfobloxConnector) DeleteObject(string) (string, error) {
	return c.fakeRefReturn, nil
}

func (c *fakeInfobloxConnector) UpdateObject(ibclient.IBObject, string) (string, error) {
	return c.fakeRefReturn, nil
}
