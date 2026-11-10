package fact

// Clone returns a copy of the fact that shares nothing with the original.
func (f *Fact) Clone() *Fact {
	if f == nil {
		return nil
	}
	c := *f
	c.Tags = append([]string(nil), f.Tags...)
	c.Check = f.Check.Clone()
	if f.Last != nil {
		last := *f.Last
		c.Last = &last
	}
	if f.Retracted != nil {
		r := *f.Retracted
		c.Retracted = &r
	}
	c.History = append([]Evidence(nil), f.History...)
	return &c
}

// Clone returns a copy of the check, or nil for no check.
func (c *Check) Clone() *Check {
	if c == nil {
		return nil
	}
	d := *c
	d.Argv = append([]string(nil), c.Argv...)
	return &d
}
