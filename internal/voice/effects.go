package voice

// drainTurnEffects 取走当前可读的副作用，不等待新数据，也不关闭通道。
func drainTurnEffects(ch <-chan TurnEffect) []TurnEffect {
	var effects []TurnEffect
	for {
		select {
		case effect, ok := <-ch:
			if !ok {
				return effects
			}
			effects = append(effects, effect)
		default:
			return effects
		}
	}
}
