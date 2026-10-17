package av1

// coder takes symbols: a symbolWriter codes them, a costCoder adds up
// their rates (from the CDFs as they are, adapting nothing), for the
// encoder's decisions.
type coder interface {
	symbol(s int, cdf []uint16)
	encode(s int, cdf []uint16, n int)
	boolean(b bool)
	literal(v uint32, n int)
}

// costCoder sums rates, in 1/costScale bits.
type costCoder struct{ n int64 }

func (c *costCoder) symbol(s int, cdf []uint16)        { c.n += int64(symbolCost(s, cdf)) }
func (c *costCoder) encode(s int, cdf []uint16, _ int) { c.n += int64(symbolCost(s, cdf)) }
func (c *costCoder) boolean(bool)                      { c.n += costScale }
func (c *costCoder) literal(_ uint32, n int)           { c.n += int64(n) * costScale }
